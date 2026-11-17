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
