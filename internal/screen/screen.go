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
// to reply (which may be nil to discard them). The drain goroutine lives as
// long as the process: vt.Emulator.Close races with a blocked Read (it writes
// the closed flag unsynchronised), so Screen has no Close. One goroutine per
// Screen is the cost.
func New(cols, rows int, reply io.Writer) *Screen {
	s := &Screen{em: vt.NewSafeEmulator(cols, rows)}
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

func isRule(em *vt.SafeEmulator, y, w int) bool {
	if cellContent(em, 0, y) != rule {
		return false
	}
	n := 0
	for x := 0; x < w; x++ {
		if cellContent(em, x, y) == rule {
			n++
		}
	}
	return float64(n) >= ruleRatio*float64(w)
}
