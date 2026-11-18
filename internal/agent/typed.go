package agent

// typedScanner reads the bytes the user sends through Write and tracks one
// fact: is there text the user typed that Enter has not sent yet. It exists
// because the screen cannot tell a draft from a prompt suggestion: Claude
// Code draws a suggestion in the empty box as plain text, with no faint
// style. The daemon proxies all user input, so it knows what was typed.
//
// Escape sequences (CSI, SS3, OSC, DCS and the other string sequences) are
// skipped: focus reports, mouse reports, cursor keys and terminal query
// replies are not typing. Text inside a bracketed paste counts. A "\r" clears
// the flag. Backspace, Ctrl-U and Ctrl-C do not clear it, so a held message
// waits rather than being pasted into text the user may still have.
//
// The parser state carries over between calls, because a browser can split
// one sequence over two writes. It is not safe for concurrent use.
type typedScanner struct {
	state  scanState
	params []byte // CSI parameter bytes, to spot ESC [ 200 ~
	paste  int    // bytes of "ESC [ 2 0 1 ~" matched inside a paste
	skip   int    // raw bytes still to skip after a legacy mouse report
	typed  bool
}

type scanState uint8

const (
	scanGround scanState = iota
	scanEsc
	scanCSI
	scanSS3
	scanString    // OSC, DCS, SOS, PM, APC: until BEL or ST
	scanStringEsc // ESC seen inside a string sequence
	scanPaste
)

const pasteEnd = "\x1b[201~"

// feed scans p in order. It returns whether any printable character was seen
// (typed text, or text inside a paste), so the caller can raise the flag
// before the bytes reach the PTY. s.typed holds the result after the last byte.
func (s *typedScanner) feed(p []byte) (sawText bool) {
	for _, b := range p {
		if s.step(b) {
			sawText = true
		}
	}
	return sawText
}

// printable reports whether a byte is part of a printable character. Bytes
// of 0x80 and above belong to UTF-8 characters.
func printable(b byte) bool { return b >= 0x20 && b != 0x7f }

func (s *typedScanner) step(b byte) (text bool) {
	if s.skip > 0 {
		s.skip--
		return false
	}
	switch s.state {
	case scanGround:
		switch {
		case b == 0x1b:
			s.state = scanEsc
		case b == '\r':
			s.typed = false
		case printable(b):
			s.typed = true
			return true
		}
	case scanEsc:
		switch b {
		case '[':
			s.state = scanCSI
			s.params = s.params[:0]
		case 'O':
			s.state = scanSS3
		case ']', 'P', 'X', '^', '_':
			s.state = scanString
		case 0x1b:
			// A second ESC starts the next sequence.
		default:
			s.state = scanGround // Alt plus a key: not text
		}
	case scanSS3:
		s.state = scanGround
	case scanCSI:
		switch {
		case b >= 0x30 && b <= 0x3f, b >= 0x20 && b <= 0x2f:
			if len(s.params) < 8 {
				s.params = append(s.params, b)
			}
		case b >= 0x40 && b <= 0x7e:
			switch {
			case b == '~' && string(s.params) == "200":
				s.state = scanPaste
				s.paste = 0
			case b == 'M' && len(s.params) == 0:
				s.state = scanGround
				s.skip = 3 // legacy mouse report: three raw bytes follow
			default:
				s.state = scanGround
			}
		case b == 0x1b:
			s.state = scanEsc // an aborted sequence
		default:
			s.state = scanGround
		}
	case scanString:
		switch b {
		case 0x07:
			s.state = scanGround
		case 0x1b:
			s.state = scanStringEsc
		}
	case scanStringEsc:
		if b == '\\' {
			s.state = scanGround
		} else {
			s.state = scanEsc
			return s.step(b)
		}
	case scanPaste:
		if b == pasteEnd[s.paste] {
			s.paste++
			if s.paste == len(pasteEnd) {
				s.state = scanGround
				s.paste = 0
			}
			return false
		}
		if s.paste > 0 {
			s.paste = 0
			if b == pasteEnd[0] {
				s.paste = 1
				return false
			}
		}
		if printable(b) {
			s.typed = true
			return true
		}
	}
	return false
}
