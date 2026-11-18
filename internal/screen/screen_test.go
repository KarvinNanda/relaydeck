package screen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) (data []byte, cols, rows int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".raw"))
	if err != nil {
		t.Fatal(err)
	}
	sz, err := os.ReadFile(filepath.Join("testdata", name+".size"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscanf(string(sz), "%d %d", &cols, &rows); err != nil {
		t.Fatal(err)
	}
	return data, cols, rows
}

func stateOf(t *testing.T, name string) State {
	t.Helper()
	data, cols, rows := fixture(t, name)
	s := New(cols, rows, nil)
	if _, err := s.Write(data); err != nil {
		t.Fatal(err)
	}
	return s.State()
}

func TestStateFromFixtures(t *testing.T) {
	tests := []struct {
		name                               string
		visible, empty, busy, trust, ready bool
	}{
		{"prompt-empty", true, true, false, false, true},
		{"prompt-draft", true, false, false, false, false},
		{"help-open", false, false, false, false, false},
		{"resume-picker", false, false, false, false, false},
		{"busy", true, true, true, false, false},
		{"ask-dialog", false, false, false, false, false},
		{"after-cancel", true, true, false, false, true},
		{"trust-dialog", false, false, false, true, false},
		// Captured at 60 columns, the narrowest PTY width relaydeck uses.
		{"prompt-empty-60", true, true, false, false, true},
		{"prompt-draft-60", true, false, false, false, false},
		{"nudge-pasted-60", true, false, false, false, false},
		{"busy-60", true, true, true, false, false},
		{"trust-dialog-60", false, false, false, true, false},
		// relaydeck starts claude with --name ws/folder, and Claude Code prints
		// that name inside the top rule of the input box.
		{"prompt-empty-named-77", true, true, false, false, true},
		{"prompt-draft-named-77", true, false, false, false, false},
		// After a turn Claude Code sets the window title with a UTF-8 spinner
		// glyph; the emulator printed the rest of that OSC onto the prompt row.
		{"prompt-idle-titled-100", true, true, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stateOf(t, tc.name)
			if got.InputVisible != tc.visible {
				t.Errorf("InputVisible = %v, want %v", got.InputVisible, tc.visible)
			}
			if got.Busy != tc.busy {
				t.Errorf("Busy = %v, want %v", got.Busy, tc.busy)
			}
			if got.Trust != tc.trust {
				t.Errorf("Trust = %v, want %v", got.Trust, tc.trust)
			}
			if got.Ready() != tc.ready {
				t.Errorf("Ready = %v, want %v", got.Ready(), tc.ready)
			}
			if tc.visible && got.InputEmpty != tc.empty {
				t.Errorf("InputEmpty = %v, want %v", got.InputEmpty, tc.empty)
			}
		})
	}
}

func TestResizeKeepsDetection(t *testing.T) {
	data, cols, rows := fixture(t, "prompt-empty")
	s := New(cols, rows, nil)
	s.Write(data)
	if !s.State().Ready() {
		t.Fatal("not ready before resize")
	}
	s.Resize(60, 30)
	// Claude Code redraws at the new width. The recorded stream is laid out
	// for 100 columns, so build a 60 column frame by hand: rule, input row
	// (prompt mark, NBSP, faint placeholder), rule, footer.
	rule60 := strings.Repeat("─", 60)
	frame := "\x1b[2J" +
		"\x1b[27;1H" + rule60 +
		"\x1b[28;1H❯\u00a0\x1b[2mTry \"fix lint errors\"\x1b[22m" +
		"\x1b[29;1H" + rule60 +
		"\x1b[30;3H⏵⏵ auto mode on"
	s.Write([]byte(frame))
	if !s.State().Ready() {
		t.Fatalf("not ready after resize and redraw: %+v", s.State())
	}
}

func TestReplyIsDrainedAndForwarded(t *testing.T) {
	var got safeBuf
	s := New(100, 30, &got)
	// Device Attributes query: the emulator must answer, and Write must not block.
	if _, err := s.Write([]byte("\x1b[c")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool { return got.Len() > 0 }) {
		t.Fatal("no reply forwarded")
	}
	// nil reply writer must also not block.
	s2 := New(100, 30, nil)
	for i := 0; i < 100; i++ {
		s2.Write([]byte("\x1b[c"))
	}
}

func TestConcurrentWriteAndState(t *testing.T) {
	data, cols, rows := fixture(t, "prompt-empty")
	s := New(cols, rows, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			s.Write(data)
		}
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		default:
			s.State()
		}
	}
}

func boxFrame(boxTop int, below ...string) []byte {
	r := strings.Repeat("─", 100)
	var b strings.Builder
	b.WriteString("\x1b[2J")
	fmt.Fprintf(&b, "\x1b[%d;1H%s", boxTop, r)
	fmt.Fprintf(&b, "\x1b[%d;1H❯ \x1b[2mTry \"x\"\x1b[22m", boxTop+1)
	fmt.Fprintf(&b, "\x1b[%d;1H%s", boxTop+2, r)
	for i, l := range below {
		fmt.Fprintf(&b, "\x1b[%d;3H%s", boxTop+3+i, l)
	}
	return []byte(b.String())
}

func TestSyntheticBoxReady(t *testing.T) {
	s := New(100, 30, nil)
	s.Write(boxFrame(27, "footer"))
	if !s.State().Ready() {
		t.Fatalf("anchored box with footer should be Ready: %+v", s.State())
	}
}

func TestOverlayBelowBoxNotReady(t *testing.T) {
	s := New(100, 30, nil)
	// Box at rows 22-24, then a 5 row panel below it with box-drawing chars.
	s.Write(boxFrame(22, "╭──────╮", "│ menu │", "│ menu │", "│ menu │", "╰──────╯"))
	if st := s.State(); st.Ready() {
		t.Fatalf("overlay below box must not be Ready: %+v", st)
	}
}

func TestBoxNotAnchoredNotReady(t *testing.T) {
	s := New(100, 30, nil)
	s.Write(boxFrame(24)) // lower rule at row 26 of 30, blank rows below
	if st := s.State(); st.Ready() {
		t.Fatalf("box not anchored to the bottom must not be Ready: %+v", st)
	}
}

func TestScrollbackIsBounded(t *testing.T) {
	s := New(100, 30, nil)
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "line %d\r\n", i)
	}
	s.Write([]byte(b.String()))
	if n := s.em.ScrollbackLen(); n > 1 {
		t.Fatalf("scrollback length = %d, want <= 1", n)
	}
}

func TestPanelCharsBelowAnchoredBoxNotReady(t *testing.T) {
	s := New(100, 30, nil)
	// Lower rule at 1-based row 28 (index rows-3), so the anchoring rule
	// passes. Only the box-drawing chars in the row below can fail it.
	s.Write(boxFrame(26, "╭────╮", "footer"))
	if st := s.State(); st.Ready() {
		t.Fatalf("panel chars below an anchored box must not be Ready: %+v", st)
	}
}

// labelledFrame draws an input box whose top rule is the given row (width w),
// with an empty prompt and a plain lower rule anchored above one footer row.
func labelledFrame(w, h int, top string) []byte {
	var b strings.Builder
	b.WriteString("\x1b[2J")
	fmt.Fprintf(&b, "\x1b[%d;1H%s", h-3, top)
	fmt.Fprintf(&b, "\x1b[%d;1H❯"+nbsp, h-2)
	fmt.Fprintf(&b, "\x1b[%d;1H%s", h-1, strings.Repeat("─", w))
	fmt.Fprintf(&b, "\x1b[%d;3Hfooter", h)
	return []byte(b.String())
}

func TestLabelledTopRule(t *testing.T) {
	label := func(w int, name string) string {
		return strings.Repeat("─", w-len(name)-3) + " " + name + " ─"
	}
	tests := []struct {
		name  string
		w     int
		top   string
		ready bool
	}{
		{"short name at 60 cols", 60, label(60, "demo/be"), true},
		{"long name at 60 cols", 60, label(60, "game-lounge/fe-customer"), true},
		{"two labels", 80, strings.Repeat("─", 20) + " a " + strings.Repeat("─", 20) + " b " + strings.Repeat("─", 31), false},
		{"label with box chars", 80, strings.Repeat("─", 60) + " │menu│ " + strings.Repeat("─", 12), false},
		{"label at the end without closing rule", 80, strings.Repeat("─", 70) + " demo/be", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(tc.w, 30, nil)
			if _, err := s.Write(labelledFrame(tc.w, 30, tc.top)); err != nil {
				t.Fatal(err)
			}
			if got := s.State().Ready(); got != tc.ready {
				t.Fatalf("Ready = %v, want %v (%+v)", got, tc.ready, s.State())
			}
		})
	}
}

// Output written for a taller screen can arrive after a resize and set a
// scroll region below the last row. The emulator panics on the next scroll;
// that panic ran in the PTY read goroutine and would end the daemon.
func TestLateScrollRegionDoesNotPanic(t *testing.T) {
	for name, late := range map[string]string{
		"scroll up":   "\x1b[1;40r\x1b[S",
		"delete line": "\x1b[3;40r\x1b[5;1H\x1b[M",
	} {
		t.Run(name, func(t *testing.T) {
			s := New(100, 40, nil)
			s.Resize(60, 10)
			if _, err := s.Write([]byte(late)); err != nil {
				t.Fatalf("Write error: %v", err)
			}
			// The screen keeps working: a full redraw is detected again.
			if _, err := s.Write(labelledFrame(60, 10, strings.Repeat("─", 60))); err != nil {
				t.Fatal(err)
			}
			if !s.State().Ready() {
				t.Fatalf("redraw after recovery should be Ready: %+v", s.State())
			}
		})
	}
}

func TestLabelledTopRuleLimits(t *testing.T) {
	tests := []struct {
		name string
		w    int
		top  string
	}{
		// Fewer than minRuleCells rule characters.
		{"few rule cells", 60, strings.Repeat("─", 4) + " " + strings.Repeat("x", 49) + " ─────"},
		// The label is longer than two thirds of the width.
		{"label too long", 80, strings.Repeat("─", 12) + " " + strings.Repeat("x", 58) + " " + strings.Repeat("─", 8)},
		// The rule stops short of 90% of the width.
		{"rule too short", 80, strings.Repeat("─", 50) + " demo/be ─"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(tc.w, 30, nil)
			if _, err := s.Write(labelledFrame(tc.w, 30, tc.top)); err != nil {
				t.Fatal(err)
			}
			if s.State().InputVisible {
				t.Fatalf("row %q must not count as the box top rule", tc.top)
			}
		})
	}
}

// A dialog option row between two labelled rules has non-faint text, so it
// never counts as an empty input box.
func TestDialogRowBetweenLabelledRulesNotReady(t *testing.T) {
	w, h := 80, 30
	top := strings.Repeat("─", w-10) + " demo/be ─"
	var b strings.Builder
	b.WriteString("\x1b[2J")
	fmt.Fprintf(&b, "\x1b[%d;1H%s", h-3, top)
	fmt.Fprintf(&b, "\x1b[%d;1H❯"+nbsp+"1. Yes", h-2)
	fmt.Fprintf(&b, "\x1b[%d;1H%s", h-1, strings.Repeat("─", w))
	fmt.Fprintf(&b, "\x1b[%d;3Hfooter", h)
	s := New(w, h, nil)
	if _, err := s.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	if s.State().Ready() {
		t.Fatalf("dialog row must not be Ready: %+v", s.State())
	}
}

func TestOSCWithUTF8IsNotPrinted(t *testing.T) {
	row := func(s *Screen) string { return strings.TrimSpace(rowText(s.em, 4, 40)) }
	tests := map[string][]string{
		"title BEL":           {"\x1b[5;3H\x1b]0;✳ demo/x\x07"},
		"title ST":            {"\x1b[5;3H\x1b]0;✳ demo/x\x1b\\"},
		"split after ESC":     {"\x1b[5;3H\x1b", "]0;✳ demo/x\x07"},
		"split inside glyph":  {"\x1b[5;3H\x1b]0;\xe2\x9c", "\xb3 demo/x\x07"},
		"split before ST end": {"\x1b[5;3H\x1b]0;✳ demo/x\x1b", "\\"},
	}
	for name, parts := range tests {
		t.Run(name, func(t *testing.T) {
			s := New(40, 10, nil)
			for _, p := range parts {
				if n, err := s.Write([]byte(p)); err != nil || n != len(p) {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			if got := row(s); got != "" {
				t.Fatalf("row 5 = %q, want empty", got)
			}
		})
	}
}

func TestPlainTextAndASCIIOSCStillReachEmulator(t *testing.T) {
	s := New(40, 10, nil)
	// An ASCII title is passed through (the emulator handles it), and text
	// after it, including an ESC that starts a CSI, is not lost.
	s.Write([]byte("\x1b]0;plain\x07\x1b[5;3Hhello ✳"))
	if got := strings.TrimSpace(rowText(s.em, 4, 40)); got != "hello ✳" {
		t.Fatalf("row 5 = %q, want %q", got, "hello ✳")
	}
}

func TestUnterminatedOSCIsBounded(t *testing.T) {
	s := New(40, 10, nil)
	s.Write([]byte("\x1b]0;✳ " + strings.Repeat("x", 10000)))
	// After the cap the filter gives up on the sequence; later output shows.
	s.Write([]byte("\x07\x1b[5;1Hafter"))
	if got := strings.TrimSpace(rowText(s.em, 4, 40)); got != "after" {
		t.Fatalf("row 5 = %q, want %q", got, "after")
	}
}
