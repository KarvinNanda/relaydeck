package agent

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/launch"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeclaude")
	if err != nil {
		panic(err)
	}
	fakeBin = filepath.Join(dir, "fakeclaude")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", fakeBin, "./testdata/fakeclaude").CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func newMgr(t *testing.T, onEnter func(string)) *Manager {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY is not reliable on CI runners; PTY tests run on unix only")
	}
	return NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), onEnter)
}

func start(t *testing.T, m *Manager, id string) *Agent {
	t.Helper()
	a, err := m.Start(id, launch.Command{Argv: []string{fakeBin}, Env: os.Environ(), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(time.Second) })
	return a
}

// collect reads from out until want appears in the accumulated bytes.
func collect(t *testing.T, acc []byte, out <-chan []byte, want string) []byte {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !bytes.Contains(acc, []byte(want)) {
		select {
		case b, ok := <-out:
			if !ok {
				t.Fatalf("channel closed before %q, got %q", want, acc)
			}
			acc = append(acc, b...)
		case <-deadline:
			t.Fatalf("timeout waiting for %q, got %q", want, acc)
		}
	}
	return acc
}

func TestStartAndEcho(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	replay, out, cancel := a.Subscribe()
	defer cancel()
	acc := collect(t, replay, out, "READY")
	if err := a.Inject([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	collect(t, acc, out, "ECHO:hello")
	if got, ok := m.Get("a"); !ok || got != a {
		t.Fatal("Get did not return the agent")
	}
}

func TestTwoSubscribersSeeSameOutput(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	r1, o1, c1 := a.Subscribe()
	r2, o2, c2 := a.Subscribe()
	defer c1()
	defer c2()
	a.Inject([]byte("same\r"))
	collect(t, r1, o1, "ECHO:same")
	collect(t, r2, o2, "ECHO:same")
}

func TestReplayAfterLateSubscribe(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	_, o, c := a.Subscribe()
	collect(t, nil, o, "READY")
	c()
	replay, _, c2 := a.Subscribe()
	defer c2()
	if !bytes.Contains(replay, []byte("READY")) {
		t.Fatalf("replay missing READY: %q", replay)
	}
}

func TestUserEnterCallback(t *testing.T) {
	var n atomic.Int32
	m := newMgr(t, func(id string) {
		if id == "a" {
			n.Add(1)
		}
	})
	a := start(t, m, "a")
	if err := a.Write([]byte("abc\r")); err != nil {
		t.Fatal(err)
	}
	if err := a.Inject([]byte("x\r")); err != nil {
		t.Fatal(err)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("callback count = %d, want 1", got)
	}
}

func TestSameDirTwiceRejected(t *testing.T) {
	m := newMgr(t, nil)
	dir := t.TempDir()
	cmd := launch.Command{Argv: []string{fakeBin}, Env: os.Environ(), Dir: dir}
	a, err := m.Start("a", cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop(time.Second)
	cmd.Dir = dir + "/"
	if _, err := m.Start("b", cmd); err != ErrAlreadyRunning {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	// After the first one stops, the same dir can start again, same id too.
	a.Stop(time.Second)
	b, err := m.Start("a", cmd)
	if err != nil {
		t.Fatalf("restart after stop: %v", err)
	}
	b.Stop(time.Second)
}

func exitEvent(t *testing.T, ev <-chan Event) Event {
	t.Helper()
	for {
		select {
		case e := <-ev:
			if e.Status == StatusStopped {
				return e
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no stopped event")
		}
	}
}

func TestExitEmitsStopped(t *testing.T) {
	m := newMgr(t, nil)
	ev, cancel := m.Events()
	defer cancel()
	a := start(t, m, "a")
	if a.Status() != StatusStarting {
		t.Fatalf("initial status = %s", a.Status())
	}
	a.Inject([]byte("fail\r"))
	e := exitEvent(t, ev)
	if e.AgentID != "a" || e.ExitCode == nil || *e.ExitCode != 3 {
		t.Fatalf("event = %+v", e)
	}
	if a.Status() != StatusStopped || a.ExitCode() != 3 {
		t.Fatalf("status %s code %d", a.Status(), a.ExitCode())
	}
	if err := a.Inject([]byte("x")); err != ErrStopped {
		t.Fatalf("inject after exit: %v", err)
	}
}

func TestSetStatusEmitsEvent(t *testing.T) {
	m := newMgr(t, nil)
	ev, cancel := m.Events()
	defer cancel()
	start(t, m, "a")
	m.SetStatus("a", StatusBusy)
	select {
	case e := <-ev:
		if e.Status != StatusBusy || e.AgentID != "a" {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	m.SetStatus("missing", StatusIdle) // must not panic
}

func TestResize(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	_, out, cancel := a.Subscribe()
	defer cancel()
	collect(t, nil, out, "READY")
	if err := a.Resize(90, 30); err != nil {
		t.Fatal(err)
	}
	collect(t, nil, out, "SIZE:90x30")
}

func TestStopSendsExit(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	start := time.Now()
	if err := a.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Stop waited for the grace period instead of the /exit")
	}
	if a.Status() != StatusStopped || a.ExitCode() != 0 {
		t.Fatalf("status %s code %d", a.Status(), a.ExitCode())
	}
}

func TestStopKillsAfterGrace(t *testing.T) {
	m := newMgr(t, nil)
	// sleep ignores /exit, so only the kill can end it.
	a, err := m.Start("s", launch.Command{Argv: []string{"sleep", "30"}, Env: os.Environ(), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(200 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("process still running after Stop")
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	_, out, cancel := a.Subscribe()
	defer cancel()
	// Never read from out. Flood until the buffer overflows.
	for i := 0; i < subBuffer+50; i++ {
		a.publish([]byte("x"))
	}
	n := 0
	for range out { // the channel must be closed, so this ends
		n++
	}
	if n > subBuffer {
		t.Fatalf("read %d chunks, buffer is %d", n, subBuffer)
	}
}

func TestRingKeepsLastBytes(t *testing.T) {
	r := newRing(8)
	r.Write([]byte("abcdef"))
	r.Write([]byte("ghij"))
	if got := string(r.Bytes()); got != "cdefghij" {
		t.Fatalf("got %q", got)
	}
	r.Write([]byte("0123456789"))
	if got := string(r.Bytes()); got != "23456789" {
		t.Fatalf("got %q", got)
	}
}

func TestStartWritesFiles(t *testing.T) {
	m := newMgr(t, nil)
	dir := t.TempDir()
	f := filepath.Join(dir, "sub", "settings.json")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte("old"), 0o644)
	a, err := m.Start("a", launch.Command{
		Argv: []string{fakeBin}, Env: os.Environ(), Dir: t.TempDir(),
		Files: map[string][]byte{f: []byte("new"), filepath.Join(dir, "deep", "x", "y"): []byte("y")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop(time.Second)
	if b, _ := os.ReadFile(f); string(b) != "new" {
		t.Fatalf("not overwritten: %q", b)
	}
	for _, p := range []string{f, filepath.Join(dir, "deep", "x", "y")} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	st, _ := os.Stat(filepath.Join(dir, "deep", "x"))
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", st.Mode().Perm())
	}
}

func TestScreenFedFromOutput(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	_, out, cancel := a.Subscribe()
	defer cancel()
	acc := collect(t, nil, out, "READY")
	if a.ScreenState().Busy {
		t.Fatal("Busy before any output of the busy marker")
	}
	a.Inject([]byte("busy\r"))
	collect(t, acc, out, "esc to interrupt")
	deadline := time.Now().Add(2 * time.Second)
	for !a.ScreenState().Busy && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !a.ScreenState().Busy {
		t.Fatal("screen state did not change after PTY output")
	}
}

// answered runs fakeclaude's cursor position query and reports whether the
// screen's reply reached the PTY (fakeclaude then reads it as an input line
// and echoes it with its escape byte).
func answered(t *testing.T, subscribed bool) bool {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	replay, out, cancel := a.Subscribe()
	collect(t, replay, out, "READY")
	if !subscribed {
		cancel()
	}
	a.Inject([]byte("query\r"))
	time.Sleep(500 * time.Millisecond) // let the reply travel
	a.Inject([]byte("\r"))             // ends the line the reply started
	time.Sleep(500 * time.Millisecond)
	a.Inject([]byte("end\r"))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _, c := a.Subscribe()
		c()
		if bytes.Contains(r, []byte("ECHO:end")) {
			return bytes.Contains(r, []byte("ECHO:\x1b["))
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("never saw ECHO:end")
	return false
}

func TestReplyOnlyWithoutSubscribers(t *testing.T) {
	if !answered(t, false) {
		t.Fatal("reply did not reach the PTY with no subscriber")
	}
	if answered(t, true) {
		t.Fatal("reply reached the PTY while a subscriber was attached")
	}
}
