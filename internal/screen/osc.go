package screen

// maxOSC bounds how much of one OSC sequence the filter holds back. Longer
// sequences are dropped whole, so an unterminated OSC cannot hide output
// forever.
const maxOSC = 4096

// oscFilter removes OSC sequences (ESC ] ... BEL or ESC \) that contain bytes
// outside ASCII before they reach the emulator. The vt parser aborts such a
// sequence at the first non-ASCII byte and prints the rest as text. Claude
// Code sets the window title with a UTF-8 spinner glyph ("✳ ws/folder")
// after every turn, so the title showed up on the prompt row and read as a
// draft. ASCII OSC sequences pass through unchanged. The filter keeps state
// across writes, because a sequence can be split between PTY reads.
type oscFilter struct {
	esc  bool   // the previous byte was ESC outside an OSC
	in   bool   // inside an OSC sequence
	end  bool   // inside an OSC, the previous byte was ESC
	skip bool   // the OSC grew past maxOSC: drop it up to its terminator
	buf  []byte // the OSC held back so far, starting with ESC ]
	wide bool   // buf holds a byte outside ASCII
}

func (f *oscFilter) filter(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch {
		case f.in:
			out = f.inOSC(out, b)
		case f.esc:
			f.esc = false
			if b == ']' {
				f.in, f.buf, f.wide, f.skip = true, append(f.buf[:0], 0x1b, ']'), false, false
				continue
			}
			out = append(out, 0x1b)
			if b == 0x1b {
				f.esc = true
				continue
			}
			out = append(out, b)
		case b == 0x1b:
			f.esc = true
		default:
			out = append(out, b)
		}
	}
	return out
}

// inOSC handles one byte inside an OSC sequence and returns out, extended by
// the whole sequence when it ends and holds only ASCII.
func (f *oscFilter) inOSC(out []byte, b byte) []byte {
	if !f.skip {
		f.buf = append(f.buf, b)
		if b >= 0x80 {
			f.wide = true
		}
		if len(f.buf) > maxOSC {
			f.skip, f.buf = true, f.buf[:0]
		}
	}
	switch {
	case b == 0x07 && !f.end:
		return f.finish(out)
	case f.end && b == '\\':
		return f.finish(out)
	case b == 0x1b:
		f.end = true
	default:
		f.end = false
	}
	return out
}

func (f *oscFilter) finish(out []byte) []byte {
	if !f.skip && !f.wide {
		out = append(out, f.buf...)
	}
	f.in, f.end, f.skip, f.wide, f.buf = false, false, false, false, f.buf[:0]
	return out
}
