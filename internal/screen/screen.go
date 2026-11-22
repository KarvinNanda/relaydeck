// Package screen replays PTY output into a headless terminal emulator and
// reads the bottom of the screen. Hooks cannot see overlays, dialogs or a
// half-typed prompt, so the daemon asks the screen before typing a nudge.
//
// The layout rules match Claude Code 2.1.326. If a new version changes the
// layout, no rule matches, InputVisible is false and delivery waits (safe
// failure).
package screen

import (
	"io"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

const (
	searchRows = 8   // only the last rows are searched for the input box
	nbsp       = " " // Claude Code puts a no-break space after the prompt mark
	rule       = "─"
	ruleRatio  = 0.9
	// minRuleCells is the least number of rule characters in a rule that
	// carries a label, so a short label line is not taken for a rule.
	minRuleCells = 10
	trustSpan    = 3 // rows between the two trust dialog options, at most
	boxChars     = "│╭╮╰╯┌┐└┘├┤┬┴┼"
)

// State is what the screen shows right now.
type State struct {
	InputVisible bool   // the input box is on screen with no overlay
	InputEmpty   bool   // no user text in the box (placeholder text does not count)
	Busy         bool   // footer shows "esc to interrupt"
	Trust        bool   // the trust dialog (Yes, I trust this folder / No, exit) is on screen
	Draft        string // text in the input box (empty when none)
}

// Ready reports whether it is safe to type a nudge.
func (s State) Ready() bool { return s.InputVisible && s.InputEmpty && !s.Busy }

// Screen is safe for concurrent Write, Resize and State calls.
type Screen struct {
	// mu keeps Write and Resize out while State reads: CellAt returns a
	// pointer into the emulator buffer, so reading its fields after the
	// emulator's own lock is released would race with a concurrent Write.
	mu    sync.Mutex
	em    *vt.SafeEmulator
	reply io.Writer
	osc   oscFilter
	// cols and rows are the size last asked for, so reset can rebuild the
	// emulator even when the old one is too broken to ask.
	cols, rows int
	closed     bool
}

// New creates a screen of the given size. The emulator answers terminal
// queries on its read side; those bytes are drained continuously and written
// to reply (which may be nil to discard them). New starts a goroutine that
// lives until Close, and reply must not block (a blocked reply stalls the
// drain and then Write).
func New(cols, rows int, reply io.Writer) *Screen {
	s := &Screen{reply: reply, cols: cols, rows: rows}
	s.em = s.newEmulator(cols, rows)
	return s
}

// Close ends the drain goroutine of the current emulator. It is safe to call
// more than once, and Write, Resize and State stay safe after it (the
// emulator answers no queries then).
//
// vt.Emulator.Close is not used: it writes its closed flag with no lock while
// the drain goroutine reads it in Read, which is a data race. Instead the
// emulator's input pipe is closed. That pipe is an io.Pipe, which is
// synchronised: the drain's Read returns EOF and the goroutine ends, and a
// later reply write fails with ErrClosedPipe instead of blocking.
func (s *Screen) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	closeDrain(s.em)
}

// closeDrain ends the drain goroutine of em. It tolerates a nil emulator.
func closeDrain(em *vt.SafeEmulator) {
	if em == nil {
		return
	}
	if pw, ok := em.InputPipe().(*io.PipeWriter); ok {
		pw.CloseWithError(io.EOF)
	}
}

// newEmulator creates an emulator and starts the goroutine that drains its
// query replies into s.reply.
func (s *Screen) newEmulator(cols, rows int) *vt.SafeEmulator {
	em := vt.NewSafeEmulator(cols, rows)
	// The detector reads only visible rows. The library ignores 0 (it keeps
	// the 10,000 line default), so 1 is the smallest scrollback.
	em.SetScrollbackSize(1)
	reply := s.reply
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := em.Read(buf)
			if n > 0 && reply != nil {
				reply.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return em
}

// Write feeds PTY output into the emulator. It never panics: output written
// for a taller screen can arrive after a resize and set a scroll region below
// the last row, and the emulator panics on the next scroll. Write runs in the
// PTY read goroutine, so such a panic would end the daemon. On a panic the
// emulator is replaced by a blank one of the same size; the detector then
// sees no input box until Claude Code redraws, which is the safe direction.
func (s *Screen) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			s.reset()
			n, err = len(p), nil
		}
	}()
	if _, err := s.em.Write(s.osc.filter(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// reset replaces a broken emulator with a blank one of the same size. The
// old emulator's drain goroutine is ended first. After Close, the new
// emulator's drain is ended at once too.
func (s *Screen) reset() {
	closeDrain(s.em)
	s.em = s.newEmulator(s.cols, s.rows)
	if s.closed {
		closeDrain(s.em)
	}
}

// Resize changes the emulator size. Claude Code redraws after a resize, so
// the caller should keep feeding PTY output. Like Write, it never panics: on
// a panic the emulator is replaced by a blank one of the new size.
func (s *Screen) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cols, s.rows = cols, rows
	defer func() {
		if r := recover(); r != nil {
			s.reset()
		}
	}()
	s.em.Resize(cols, rows)
}

// Size returns the size last given to New or Resize.
func (s *Screen) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// State reads the current screen. Like Write, it never panics: on a panic it
// returns the zero State (no input box, so delivery waits) and replaces the
// emulator by a blank one.
func (s *Screen) State() (st State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			s.reset()
			st = State{}
		}
	}()
	w, h := s.em.Width(), s.em.Height()
	rows := make([]string, h)
	for y := 0; y < h; y++ {
		rows[y] = rowText(s.em, y, w)
	}
	first := h - searchRows
	if first < 0 {
		first = 0
	}
	for y := 0; y < h; y++ {
		if strings.Contains(rows[y], "Yes, I trust this folder") && trustLayout(rows, y) {
			st.Trust = true
		}
		if y >= first && strings.Contains(rows[y], "esc to interrupt") {
			st.Busy = true
		}
	}
	// The lower rule must be in the last searchRows rows; the prompt row may
	// be higher when the draft spans several rows.
	for lower := first + 2; lower < h; lower++ {
		if !isRule(s.em, lower, w) {
			continue
		}
		draft, ok := inputBox(s.em, w, lower)
		if !ok || !anchored(rows, lower) {
			continue
		}
		st.InputVisible = true
		st.Draft = draft
		st.InputEmpty = draft == ""
		break
	}
	return st
}

// inputBox reads the input box whose lower rule is at row lower: a rule, the
// prompt row ("❯" and a no-break space), any continuation rows, and the rule
// at lower. A continuation row is a draft with newlines (Shift+Enter) or a
// long line Claude Code wraps: it starts with two blank cells and has no rule
// or box-drawing character. Anything else above the lower rule means it is
// not the input box. The draft is the text of every row after the prompt
// mark, one line per row; faint text (the placeholder) does not count.
func inputBox(em *vt.SafeEmulator, w, lower int) (string, bool) {
	for y := lower - 1; y >= 1; y-- {
		if cellContent(em, 0, y) == "❯" && cellContent(em, 1, y) == nbsp {
			if !isRule(em, y-1, w) {
				return "", false
			}
			lines := make([]string, 0, lower-y)
			for r := y; r < lower; r++ {
				lines = append(lines, strings.TrimRight(rowDraft(em, w, r), " "))
			}
			return strings.TrimSpace(strings.Join(lines, "\n")), true
		}
		if !continuation(em, w, y) {
			return "", false
		}
	}
	return "", false
}

// continuation reports whether row y can be a continuation row of a draft.
func continuation(em *vt.SafeEmulator, w, y int) bool {
	if !blankCell(cellContent(em, 0, y)) || !blankCell(cellContent(em, 1, y)) {
		return false
	}
	for x := 2; x < w; x++ {
		if c := cellContent(em, x, y); c == rule || strings.ContainsAny(c, boxChars) {
			return false
		}
	}
	return true
}

func blankCell(c string) bool { return c == "" || c == " " || c == nbsp }

// rowDraft is the text of row y after the two prompt or indent cells, with
// faint cells as spaces.
func rowDraft(em *vt.SafeEmulator, w, y int) string {
	var b strings.Builder
	for x := 2; x < w; x++ {
		c := em.CellAt(x, y)
		if c == nil || c.Content == "" || c.Style.Attrs&uv.AttrFaint != 0 {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(c.Content)
	}
	return b.String()
}

// trustLayout reports whether the "Yes, I trust this folder" row at y is an
// option of the trust dialog: the other option, "No, exit", is within
// trustSpan rows (a blank or description row may sit between the options).
// Plain text that only says the same words has no such neighbour.
func trustLayout(rows []string, y int) bool {
	for n := y - trustSpan; n <= y+trustSpan; n++ {
		if n != y && n >= 0 && n < len(rows) && strings.Contains(rows[n], "No, exit") {
			return true
		}
	}
	return false
}

// anchored reports whether the lower rule at row lower sits at the bottom of
// the screen: at most 2 rows below it (a footer that wraps), none of them
// containing box-drawing characters (a panel or menu drawn below the box).
func anchored(rows []string, lower int) bool {
	if lower < len(rows)-3 {
		return false
	}
	for _, r := range rows[lower+1:] {
		if strings.ContainsAny(r, "─│╭╮╰╯") {
			return false
		}
	}
	return true
}

func cellContent(em *vt.SafeEmulator, x, y int) string {
	c := em.CellAt(x, y)
	if c == nil {
		return ""
	}
	return c.Content
}

func rowText(em *vt.SafeEmulator, y, w int) string {
	var b strings.Builder
	for x := 0; x < w; x++ {
		c := cellContent(em, x, y)
		if c == "" {
			c = " "
		}
		b.WriteString(c)
	}
	return b.String()
}

// isRule reports whether row y is a horizontal rule across the screen.
// relaydeck starts claude with --name ws/folder, and Claude Code prints that
// name inside the top rule of the input box ("──── ws/folder ─"). So one
// label run is allowed between rule characters, as long as it holds no
// box-drawing characters, the row starts and ends with the rule character and
// spans at least ruleRatio of the width.
func isRule(em *vt.SafeEmulator, y, w int) bool {
	if cellContent(em, 0, y) != rule {
		return false
	}
	last := w - 1
	for last > 0 && strings.TrimSpace(cellContent(em, last, y)) == "" {
		last--
	}
	if cellContent(em, last, y) != rule || float64(last+1) < ruleRatio*float64(w) {
		return false
	}
	n, runs, inLabel := 0, 0, false
	for x := 0; x <= last; x++ {
		c := cellContent(em, x, y)
		if c == rule {
			n++
			inLabel = false
			continue
		}
		if strings.ContainsAny(c, boxChars) {
			return false
		}
		if !inLabel {
			runs++
			inLabel = true
		}
	}
	// A label may take at most two thirds of the width, so a line of text
	// framed by a few rule characters is not taken for a rule.
	return runs <= 1 && n >= minRuleCells && 3*n >= w
}
