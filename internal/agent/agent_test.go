package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/KarvinNanda/relaydeck/internal/launch"
	"github.com/KarvinNanda/relaydeck/internal/screen"
	"github.com/aymanbagabas/go-pty"
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
	// READY may already be in the scrollback when the first Subscribe runs,
	// so the first replay counts too.
	r, o, c := a.Subscribe()
	collect(t, r, o, "READY")
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
	if e := <-ev; e.Status != StatusStarting {
		t.Fatalf("first event = %+v, want starting", e)
	}
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
	// READY may already be in the scrollback, so the replay counts too.
	replay, out, cancel := a.Subscribe()
	defer cancel()
	collect(t, replay, out, "READY")
	if err := a.Resize(90, 30); err != nil {
		t.Fatal(err)
	}
	collect(t, nil, out, "SIZE:90x30")
}

// drawInputBox paints the empty input box at the bottom of the agent's screen
// and parks the cursor on the prompt row, so the terminal echo of what is
// typed lands in the box like it does in Claude Code.
func drawInputBox(a *Agent) {
	rule := strings.Repeat("─", defaultCols)
	a.screen.Write([]byte("\x1b[2J\x1b[37;1H" + rule + "\x1b[38;1H❯\u00a0\x1b[2mTry something\x1b[22m" +
		"\x1b[39;1H" + rule + "\x1b[40;3H? for shortcuts\x1b[38;3H\x1b[K"))
}

func TestStopSendsExit(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	replay, out, cancel := a.Subscribe()
	defer cancel()
	collect(t, replay, out, "READY") // READY is printed at the cursor, so wait for it first
	drawInputBox(a)
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
	timeout := time.After(3 * time.Second)
drain:
	for {
		select {
		case _, ok := <-out:
			if !ok {
				break drain
			}
			n++
		case <-timeout:
			t.Fatal("slow subscriber was not dropped (channel still open)")
		}
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
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	replay, out, cancel := a.Subscribe()
	defer cancel()
	acc := collect(t, replay, out, "READY") // READY may already be in the replay
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
	defer cancel()
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

func TestRestartLastEventIsNotStopped(t *testing.T) {
	m := newMgr(t, nil)
	ev, cancel := m.Events()
	defer cancel()
	dir := t.TempDir()
	cmd := launch.Command{Argv: []string{fakeBin}, Env: os.Environ(), Dir: dir}
	a, err := m.Start("a", cmd)
	if err != nil {
		t.Fatal(err)
	}
	a.Inject([]byte("quit\r"))
	<-a.Done()
	b, err := m.Start("a", cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop(time.Second)
	time.Sleep(300 * time.Millisecond)
	var last Status
	for drained := false; !drained; {
		select {
		case e := <-ev:
			if e.AgentID == "a" {
				last = e.Status
			}
		default:
			drained = true
		}
	}
	if last != StatusStarting {
		t.Fatalf("last event = %q, want %q", last, StatusStarting)
	}
}

func TestStartRequiresEnv(t *testing.T) {
	m := newMgr(t, nil)
	_, err := m.Start("a", launch.Command{Argv: []string{fakeBin}, Dir: t.TempDir()})
	if err == nil {
		t.Fatal("nil Env must be rejected so the scrubbed env is never skipped")
	}
}

// hungPty is a PTY whose Write blocks until Close, like a full input buffer.
type hungPty struct {
	pty.Pty
	release chan struct{}
}

func (h hungPty) Write([]byte) (int, error) { <-h.release; return 0, io.ErrClosedPipe }

func TestStopBoundedWhenWriteBlocks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sleep")
	}
	proc := exec.Command("sleep", "30")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		pty:  hungPty{release: make(chan struct{})},
		cmd:  &pty.Cmd{Process: proc.Process},
		done: make(chan struct{}),
	}
	go func() { proc.Wait(); close(a.done) }()
	stopped := make(chan error, 1)
	go func() { stopped <- a.Stop(200 * time.Millisecond) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		proc.Process.Kill()
		t.Fatal("Stop hung on a blocked PTY write")
	}
}

func TestWriteReachesPTY(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	replay, out, cancel := a.Subscribe()
	defer cancel()
	acc := collect(t, replay, out, "READY")
	if err := a.Write([]byte("typed\r")); err != nil {
		t.Fatal(err)
	}
	collect(t, acc, out, "ECHO:typed")
}

func TestHasDraft(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	replay, out, cancel := a.Subscribe()
	defer cancel()
	collect(t, replay, out, "READY")
	if a.HasDraft() {
		t.Fatal("no input box yet, HasDraft must be false")
	}
	feed := func(typed string) {
		rule := strings.Repeat("─", 120)
		a.screen.Write([]byte("\x1b[2J\x1b[H"))
		a.screen.Write([]byte("\x1b[37;1H" + rule + "\r\n❯ " + typed + "\r\n" + rule + "\r\n  ? for shortcuts"))
	}
	feed("hello")
	if !a.HasDraft() {
		t.Fatalf("typed text in the box, HasDraft must be true: %+v", a.ScreenState())
	}
	feed("")
	if a.HasDraft() {
		t.Fatal("empty box, HasDraft must be false")
	}
}

func TestRemoveOnlyStopped(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	if m.Remove("a") {
		t.Fatal("Remove dropped a running agent")
	}
	if _, ok := m.Get("a"); !ok {
		t.Fatal("running agent gone after refused Remove")
	}
	a.Inject([]byte("quit\r"))
	<-a.Done()
	if !m.Remove("a") {
		t.Fatal("Remove refused a stopped agent")
	}
	if _, ok := m.Get("a"); ok {
		t.Fatal("stopped agent still listed after Remove")
	}
	if m.Remove("a") || m.Remove("nope") {
		t.Fatal("Remove of an unknown id reported true")
	}
}

// Kill must not type /exit: after a draft, "/exit" would join the draft
// and could be submitted as a prompt.
func TestKillTypesNothing(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	replay, out, cancel := a.Subscribe()
	defer cancel()
	collect(t, replay, out, "READY")
	if err := a.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("process still running after Kill")
	}
	if bytes.Contains(a.ring.Bytes(), []byte("/exit")) {
		t.Fatalf("Kill typed /exit: %q", a.ring.Bytes())
	}
	if err := a.Kill(); err != nil {
		t.Fatalf("second Kill: %v", err)
	}
}

// Fix round 1 (3c): a stopped agent that was removed can be freed. The
// screen's drain goroutine must not keep the Agent (scrollback, emulator feed, PTY) reachable.
func TestRemovedAgentIsCollected(t *testing.T) {
	m := newMgr(t, nil)
	wp := func() weak.Pointer[Agent] {
		a, err := m.Start("a", launch.Command{Argv: []string{fakeBin}, Env: os.Environ(), Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		a.Inject([]byte("quit\r"))
		<-a.Done()
		if !m.Remove("a") {
			t.Fatal("Remove refused")
		}
		if len(a.ring.Bytes()) != 0 {
			t.Fatal("Remove kept the scrollback")
		}
		return weak.Make(a)
	}()
	for i := 0; i < 50 && wp.Value() != nil; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if wp.Value() != nil {
		t.Fatal("removed agent is still reachable after GC")
	}
}

// The screen must already have the new size when the PTY resize (and so the
// SIGWINCH redraw) happens.
func TestResizeScreenBeforePTY(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	colsAtPTY, called := 0, false
	real := a.ptyResize
	a.ptyResize = func(cols, rows int) error {
		colsAtPTY, _ = a.screen.Size()
		called = true
		return real(cols, rows)
	}
	if err := a.Resize(90, 30); err != nil {
		t.Fatal(err)
	}
	if !called || colsAtPTY != 90 {
		t.Fatalf("screen cols at PTY resize = %d (called %v), want 90", colsAtPTY, called)
	}
}

// The same-folder guard treats NFC and NFD spellings of one folder as the
// same. Only darwin has a file system that opens a folder by either form.
func TestSameDirNFCvsNFDRejected(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs APFS, which finds a folder by NFC or NFD name")
	}
	m := newMgr(t, nil)
	base := t.TempDir()
	nfd, nfc := base+"/café", base+"/café"
	if err := os.Mkdir(nfd, 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := m.Start("a", launch.Command{Argv: []string{fakeBin}, Env: os.Environ(), Dir: nfd})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop(time.Second)
	if _, err := m.Start("b", launch.Command{Argv: []string{fakeBin}, Env: os.Environ(), Dir: nfc}); err != ErrAlreadyRunning {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

// A symlink spelling and the real path of one directory cannot both run.
func TestSameDirSymlinkRejected(t *testing.T) {
	m := newMgr(t, nil)
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks not available")
	}
	a, err := m.Start("a", launch.Command{Argv: []string{fakeBin}, Env: os.Environ(), Dir: real})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop(time.Second)
	if _, err := m.Start("b", launch.Command{Argv: []string{fakeBin}, Env: os.Environ(), Dir: link}); err != ErrAlreadyRunning {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

// The screen is read a few ms before /exit is typed. A dialog that opens in
// that gap must not get the Enter: the agent is killed instead.
func TestStopDoesNotEnterIntoDialog(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	replay, out, cancel := a.Subscribe()
	defer cancel()
	collect(t, replay, out, "READY")
	a.screen.Write([]byte("\x1b[2J\x1b[5;1H \u276f No, exit\x1b[6;1H   Yes, I trust this folder"))
	begin := time.Now()
	if err := a.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if time.Since(begin) > 3*time.Second {
		t.Fatal("Stop waited for the grace period instead of killing")
	}
	// fakeclaude exits 0 on a typed /exit plus Enter. A kill is not 0.
	if a.ExitCode() == 0 {
		t.Fatal("Enter reached the agent while a dialog was on screen")
	}
}

// A hung claude with a full input buffer must not block Inject for good: it
// runs under the delivery lock.
func TestInjectTimesOut(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	a := &Agent{
		pty:           hungPty{release: release},
		done:          make(chan struct{}),
		injectTimeout: 50 * time.Millisecond,
	}
	begin := time.Now()
	err := a.Inject([]byte("x"))
	if !errors.Is(err, ErrInjectTimeout) {
		t.Fatalf("err = %v, want ErrInjectTimeout", err)
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatal("Inject did not return near its timeout")
	}
	// A write queued behind the stuck one is dropped, never typed late.
	if err := a.Inject([]byte("y")); !errors.Is(err, ErrInjectTimeout) {
		t.Fatalf("second Inject err = %v", err)
	}
}

// exitTyped must accept the real screens Claude Code draws once /exit is
// typed: the slash-command list shows above the box.
func TestExitTypedOnRealScreens(t *testing.T) {
	for _, name := range []string{"prompt-slash-exit-100", "prompt-slash-exit-60"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("..", "screen", "testdata")
			raw, err := os.ReadFile(filepath.Join(dir, name+".raw"))
			if err != nil {
				t.Fatal(err)
			}
			sz, err := os.ReadFile(filepath.Join(dir, name+".size"))
			if err != nil {
				t.Fatal(err)
			}
			var cols, rows int
			if _, err := fmt.Sscanf(string(sz), "%d %d", &cols, &rows); err != nil {
				t.Fatal(err)
			}
			a := &Agent{screen: screen.New(cols, rows, nil)}
			defer a.screen.Close()
			a.screen.Write(raw)
			if !a.exitTyped() {
				t.Fatalf("exitTyped false on a real /exit screen: %+v", a.screen.State())
			}
		})
	}
}

func TestUserTypedTracksKeystrokes(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	steps := []struct {
		name  string
		write string
		want  bool
	}{
		{"fresh agent", "", false},
		{"printable", "abc", true},
		{"enter clears", "\r", false},
		{"enter after text in one write", "abc\r", false},
		{"text after enter in one write", "\rabc", true},
		{"backspace does not clear", "\x7f", true},
		{"ctrl-u does not clear", "\x15", true},
		{"ctrl-c does not clear", "\x03", true},
		{"enter clears again", "\r", false},
		{"focus in", "\x1b[I", false},
		{"focus out", "\x1b[O", false},
		{"sgr mouse press", "\x1b[<0;10;5M", false},
		{"sgr mouse release", "\x1b[<0;10;5m", false},
		{"legacy mouse report", "\x1b[M !!", false},
		{"cursor position reply", "\x1b[12;5R", false},
		{"device attributes reply", "\x1b[?62;4c", false},
		{"arrow key", "\x1b[A", false},
		{"ss3 key", "\x1bOP", false},
		{"osc reply with BEL", "\x1b]11;rgb:0000/0000/0000\x07", false},
		{"osc reply with ST", "\x1b]11;rgb:0000/0000/0000\x1b\\", false},
		{"dcs reply", "\x1bP1$r0m\x1b\\", false},
		{"alt key", "\x1bb", false},
		{"utf-8 text", "café", true},
		{"enter", "\r", false},
		{"control bytes only", "\x01\x02\x05\t", false},
		{"bracketed paste", "\x1b[200~pasted text\x1b[201~", true},
		{"enter after paste", "\r", false},
		{"text after a focus report", "\x1b[Ix", true},
		{"enter", "\r", false},
		{"empty paste", "\x1b[200~\x1b[201~", false},
		{"enter inside a paste does not clear", "x\x1b[200~a\rb\x1b[201~", true},
	}
	for _, s := range steps {
		if s.write != "" {
			if err := a.Write([]byte(s.write)); err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}
		}
		if got := a.UserTyped(); got != s.want {
			t.Fatalf("%s: UserTyped = %v, want %v", s.name, got, s.want)
		}
	}
}

// A browser can split one escape sequence over two writes.
func TestUserTypedSequenceSplitOverWrites(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	for _, part := range []string{"\x1b[<0;1", "0;5M", "\x1b", "[I", "\x1b]11;rgb:0/0/0", "\x07"} {
		if err := a.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
		if a.UserTyped() {
			t.Fatalf("UserTyped set after %q", part)
		}
	}
	for _, part := range []string{"\x1b[20", "0~hi", "\x1b[20", "1~"} {
		a.Write([]byte(part))
	}
	if !a.UserTyped() {
		t.Fatal("split bracketed paste did not set UserTyped")
	}
}

func TestInjectDoesNotSetUserTyped(t *testing.T) {
	m := newMgr(t, nil)
	a := start(t, m, "a")
	if err := a.Inject([]byte("\x1b[200~[relaydeck] 1 new message. Call read_messages.\x1b[201~")); err != nil {
		t.Fatal(err)
	}
	if a.UserTyped() {
		t.Fatal("Inject set UserTyped")
	}
	a.Write([]byte("x"))
	a.Inject([]byte("\r"))
	if !a.UserTyped() {
		t.Fatal("Inject cleared UserTyped")
	}
}
