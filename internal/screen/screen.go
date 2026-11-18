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
	boxChars     = "│╭╮╰╯┌┐└┘├┤┬┴┼"
)

// State is what the screen shows right now.
type State struct {
	InputVisible bool // the input box is on screen with no overlay
	InputEmpty   bool // no user text in the box (placeholder text does not count)
	Busy         bool // footer shows "esc to interrupt"
	Trust        bool // "Yes, I trust this folder" is on screen
}

// Ready reports whether it is safe to type a nudge.
func (s State) Ready() bool { return s.InputVisible && s.InputEmpty && !s.Busy }

// Screen is safe for concurrent Write, Resize and State calls.
type Screen struct {
	// mu keeps Write and Resize out while State reads: CellAt returns a
	// pointer into the emulator buffer, so reading its fields after the
	// emulator's own lock is released would race with a concurrent Write.
	mu sync.Mutex
	em *vt.SafeEmulator
}

// New creates a screen of the given size. The emulator answers terminal
// queries on its read side; those bytes are drained continuously and written
// to reply (which may be nil to discard them). New starts a goroutine that
// lives for the process, and reply must not block (a blocked reply stalls the
// drain and then Write). vt.Emulator.Close races with a blocked Read (it writes
// the closed flag unsynchronised), so Screen has no Close. One goroutine per
// Screen is the cost.
func New(cols, rows int, reply io.Writer) *Screen {
	s := &Screen{em: vt.NewSafeEmulator(cols, rows)}
	// The detector reads only visible rows. The library ignores 0 (it keeps
	// the 10,000 line default), so 1 is the smallest scrollback.
	s.em.SetScrollbackSize(1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := s.em.Read(buf)
			if n > 0 && reply != nil {
				reply.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

// Write feeds PTY output into the emulator.
func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.em.Write(p)
}

// Resize changes the emulator size. Claude Code redraws after a resize, so
// the caller should keep feeding PTY output.
func (s *Screen) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.em.Resize(cols, rows)
}

// State reads the current screen.
func (s *Screen) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, h := s.em.Width(), s.em.Height()
	var st State
	rows := make([]string, h)
	for y := 0; y < h; y++ {
		rows[y] = rowText(s.em, y, w)
	}
	first := h - searchRows
	if first < 0 {
		first = 0
	}
	for y := 0; y < h; y++ {
		if strings.Contains(rows[y], "Yes, I trust this folder") {
			st.Trust = true
		}
		if y >= first && strings.Contains(rows[y], "esc to interrupt") {
			st.Busy = true
		}
	}
	for y := first + 1; y < h-1; y++ {
		if !isRule(s.em, y-1, w) || !isRule(s.em, y+1, w) {
			continue
		}
		if cellContent(s.em, 0, y) != "❯" || cellContent(s.em, 1, y) != nbsp {
			continue
		}
		if !anchored(rows, y+1) {
			continue
		}
		st.InputVisible = true
		st.InputEmpty = true
		for x := 2; x < w; x++ {
			c := s.em.CellAt(x, y)
			if c == nil || strings.TrimSpace(c.Content) == "" {
				continue
			}
			if c.Style.Attrs&uv.AttrFaint == 0 {
				st.InputEmpty = false
				break
			}
		}
		break
	}
	return st
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
	return runs <= 1 && n >= minRuleCells
}
