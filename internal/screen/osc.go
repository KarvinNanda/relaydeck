package screen

// maxOSC bounds how much of one OSC sequence the filter holds back. When a
// sequence grows past it, the filter stops holding it: held ASCII bytes are
// passed on, held non-ASCII bytes are dropped, and the rest of the sequence
// flows through unfiltered. So no output is ever hidden for longer than one
// capped sequence.
const maxOSC = 4096

const (
	bel = 0x07
	esc = 0x1b
	can = 0x18
	sub = 0x1a
)

// oscFilter removes OSC sequences (ESC ] ... BEL or ESC \) that contain bytes
// outside ASCII before they reach the emulator. The vt parser aborts such a
// sequence at the first non-ASCII byte and prints the rest as text. Claude
// Code sets the window title with a UTF-8 spinner glyph ("✳ ws/folder")
// after every turn, so the title showed up on the prompt row and read as a
// draft. ASCII OSC sequences pass through unchanged.
//
// The filter ends a sequence where the parser does: at BEL, at ESC \, at
// ESC followed by anything else (that ESC starts the next sequence), and at
// CAN or SUB (which abort it). It keeps state across writes, because a
// sequence can be split between PTY reads; an ESC at the very end of a write
// is held until the next write.
type oscFilter struct {
	esc  bool   // the previous byte was ESC outside an OSC
	in   bool   // inside an OSC sequence, holding it in buf
	end  bool   // inside an OSC, the previous byte was ESC
	buf  []byte // the OSC held back so far, starting with ESC ]
	wide bool   // buf holds a byte outside ASCII
}

func (f *oscFilter) filter(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		out = f.step(out, b)
	}
	return out
}

func (f *oscFilter) step(out []byte, b byte) []byte {
	if f.in {
		return f.inOSC(out, b)
	}
	if f.esc {
		f.esc = false
		if b == ']' {
			f.in, f.end, f.wide = true, false, false
			f.buf = append(f.buf[:0], esc, ']')
			return out
		}
		out = append(out, esc)
		// Fall through: b is handled as a normal byte (it may be another ESC).
	}
	if b == esc {
		f.esc = true
		return out
	}
	return append(out, b)
}

// inOSC handles one byte inside an OSC sequence.
func (f *oscFilter) inOSC(out []byte, b byte) []byte {
	if f.end {
		f.end = false
		if b == '\\' {
			f.buf = append(f.buf, esc, '\\')
			return f.close(out)
		}
		// ESC followed by anything else ends the OSC; that ESC starts the
		// next sequence, so b is handled as the byte after an ESC.
		out = f.close(f.terminate(out))
		f.esc = true
		return f.step(out, b)
	}
	switch b {
	case bel:
		f.buf = append(f.buf, bel)
		return f.close(out)
	case esc:
		f.end = true
		return out
	case can, sub:
		// The parser aborts the string and executes the control. A held
		// ASCII sequence is passed on as it is, so the emulator sees the
		// same abort.
		return append(f.close(out), b)
	}
	f.buf = append(f.buf, b)
	if b >= 0x80 {
		f.wide = true
	}
	if len(f.buf) > maxOSC {
		// Stop holding: pass ASCII on, drop non-ASCII, then let the rest of
		// this sequence through unfiltered (the emulator's own behaviour).
		if !f.wide {
			out = append(out, f.buf...)
		}
		f.in, f.buf, f.wide = false, f.buf[:0], false
	}
	return out
}

// terminate ends a held ASCII sequence with BEL, so the emulator does not run
// on into the bytes that follow. Non-ASCII sequences are simply dropped.
func (f *oscFilter) terminate(out []byte) []byte {
	if !f.wide {
		f.buf = append(f.buf, bel)
	}
	return out
}

// close emits the held sequence when it is ASCII and resets the OSC state.
func (f *oscFilter) close(out []byte) []byte {
	if !f.wide {
		out = append(out, f.buf...)
	}
	f.in, f.end, f.wide, f.buf = false, false, false, f.buf[:0]
	return out
}
