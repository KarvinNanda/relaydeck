package agent

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/screen"
	"github.com/aymanbagabas/go-pty"
)

const (
	scrollbackSize = 1 << 20
	subBuffer      = 256
	replyBuffer    = 64
	defaultCols    = 120
	defaultRows    = 40
	// defaultInjectTimeout bounds one Inject. A healthy PTY takes a write at
	// once; only a hung process with a full input buffer blocks.
	defaultInjectTimeout = 2 * time.Second
	// exitConfirmPolls and exitConfirmEvery bound the wait for "/exit" to
	// show in the input box before Stop sends Enter (500 ms).
	exitConfirmPolls = 20
	exitConfirmEvery = 25 * time.Millisecond
)

// Agent is one running process inside a PTY.
type Agent struct {
	id  string
	dir string
	log *slog.Logger

	pty pty.Pty
	// ptyResize resizes the PTY. It is a field so a test can see the order of
	// the two resizes in Resize without replacing a.pty under the reader.
	ptyResize func(cols, rows int) error
	cmd       *pty.Cmd
	screen    *screen.Screen
	ring      *ring

	onUserEnter func(id string)
	emit        func(Event)
	reply       *replyWriter

	mu       sync.Mutex
	status   Status
	subs     map[*subscriber]struct{}
	exitCode int
	stopped  bool

	// injectTimeout bounds one Inject. Zero means defaultInjectTimeout.
	injectTimeout time.Duration
	injectMu      sync.Mutex // one injected write at a time, in order

	replyCh   chan []byte
	done      chan struct{} // closed when the process is gone and output is drained
	readDone  chan struct{}
	closeOnce sync.Once
}

type subscriber struct {
	ch     chan []byte
	closed bool
}

// ID returns the agent id.
func (a *Agent) ID() string { return a.id }

// Status returns the current status.
func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// Done is closed once the process has exited and its output is drained.
func (a *Agent) Done() <-chan struct{} { return a.done }

// ExitCode returns the exit code. It is only meaningful after Done is closed.
func (a *Agent) ExitCode() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exitCode
}

// replyWriter forwards emulator query replies to the PTY, but only while no
// browser is attached (an attached xterm.js answers the same queries). It
// never blocks: the Screen drain goroutine calls it. waitLoop closes the
// Screen once the process is gone, which ends that goroutine. Until then the
// goroutine holds this writer, so the Agent is held through an atomic pointer
// that waitLoop clears; then a removed Agent can be freed.
type replyWriter struct{ a atomic.Pointer[Agent] }

func (w *replyWriter) Write(p []byte) (int, error) {
	a := w.a.Load()
	if a == nil {
		return len(p), nil
	}
	a.mu.Lock()
	attached := len(a.subs) > 0
	a.mu.Unlock()
	if !attached {
		select {
		case a.replyCh <- append([]byte(nil), p...):
		default:
		}
	}
	return len(p), nil
}

func (a *Agent) replyLoop() {
	for {
		select {
		case p := <-a.replyCh:
			a.pty.Write(p)
		case <-a.done:
			return
		}
	}
}

func (a *Agent) readLoop() {
	defer close(a.readDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := a.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			a.publish(chunk)
			a.screen.Write(chunk)
		}
		if err != nil {
			return
		}
	}
}

// publish stores the chunk in the scrollback and sends it to subscribers under
// one lock, so a new subscriber's replay and live output never overlap or
// leave a gap.
func (a *Agent) publish(chunk []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ring.Write(chunk)
	for s := range a.subs {
		select {
		case s.ch <- chunk:
		default:
			// Slow subscriber: drop it. The UI reconnects and gets the replay.
			a.dropLocked(s)
		}
	}
}

func (a *Agent) dropLocked(s *subscriber) {
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
	delete(a.subs, s)
}

// waitLoop waits for the process, drains output, then reports "stopped".
func (a *Agent) waitLoop() {
	a.cmd.Wait()
	code := -1
	if ps := a.cmd.ProcessState; ps != nil {
		code = ps.ExitCode()
	}
	// Let the reader drain what is already buffered, then close the PTY to
	// unblock it if the slave side is still held open.
	select {
	case <-a.readDone:
	case <-time.After(200 * time.Millisecond):
	}
	a.pty.Close()
	<-a.readDone
	a.screen.Close() // the reader is done, so nothing writes to the screen now

	a.mu.Lock()
	a.exitCode = code
	a.status = StatusStopped
	a.stopped = true
	for s := range a.subs {
		a.dropLocked(s)
	}
	// Emit under a.mu, before done closes: SetStatus does the same, so no
	// status event for this agent can follow "stopped", and a restart (which
	// needs done closed) cannot be overtaken by this event.
	a.emit(Event{AgentID: a.id, Status: StatusStopped, ExitCode: &code})
	close(a.done)
	a.reply.a.Store(nil) // no replies after exit; lets a removed Agent be freed
	a.mu.Unlock()
	a.log.Info("agent stopped", "agent_id", a.id, "exit_code", code)
}

// ErrInjectTimeout is returned when the PTY did not accept an injected write
// in time (a hung process with a full input buffer).
var ErrInjectTimeout = errors.New("agent: write to the PTY timed out")

// ErrStopped is returned when writing to an agent whose process is gone.
var ErrStopped = errors.New("agent: process has stopped")

func (a *Agent) isStopped() bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}

// Write sends user keystrokes to the PTY. Each "\r" is the user pressing
// Enter and fires the OnUserEnter callback.
func (a *Agent) Write(p []byte) error {
	if a.isStopped() {
		return ErrStopped
	}
	if _, err := a.pty.Write(p); err != nil {
		return err
	}
	if a.onUserEnter != nil {
		for i := bytes.Count(p, []byte{'\r'}); i > 0; i-- {
			a.onUserEnter(a.id)
		}
	}
	return nil
}

// Inject sends daemon input (the nudge) to the PTY. It never fires
// OnUserEnter.
//
// The write runs in its own goroutine and Inject gives up after the inject
// timeout with ErrInjectTimeout, because a hung process with a full input
// buffer blocks a PTY write for good, and the delivery loop calls Inject
// under its lock. Writes are ordered by injectMu. A write that is still
// queued when its Inject gave up is dropped, so a late nudge is never typed
// after the caller moved on. The one write already blocked in the PTY ends
// when the PTY closes (waitLoop closes it once the process is gone).
func (a *Agent) Inject(p []byte) error {
	if a.isStopped() {
		return ErrStopped
	}
	var abandoned atomic.Bool
	res := make(chan error, 1)
	go func() {
		a.injectMu.Lock()
		defer a.injectMu.Unlock()
		if abandoned.Load() {
			res <- ErrInjectTimeout
			return
		}
		_, err := a.pty.Write(p)
		res <- err
	}()
	timeout := a.injectTimeout
	if timeout <= 0 {
		timeout = defaultInjectTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-res:
		return err
	case <-timer.C:
		abandoned.Store(true)
		return ErrInjectTimeout
	}
}

// ScreenState returns the current reading of the terminal screen.
func (a *Agent) ScreenState() screen.State { return a.screen.State() }

// HasDraft reports whether the input box holds text the user has typed.
func (a *Agent) HasDraft() bool {
	st := a.screen.State()
	return st.InputVisible && !st.InputEmpty
}

// Resize resizes the PTY and the screen emulator.
func (a *Agent) Resize(cols, rows uint16) error {
	// The screen goes first: the PTY resize sends SIGWINCH, Claude Code
	// redraws at once, and that output must land on the new size.
	a.screen.Resize(int(cols), int(rows))
	return a.ptyResize(int(cols), int(rows))
}

// Subscribe returns the scrollback and a channel with later output. The
// channel is closed when the agent stops, when cancel is called, or when the
// subscriber is too slow.
func (a *Agent) Subscribe() (replay []byte, out <-chan []byte, cancel func()) {
	s := &subscriber{ch: make(chan []byte, subBuffer)}
	a.mu.Lock()
	replay = a.ring.Bytes()
	if a.stopped {
		s.closed = true
		close(s.ch)
	} else {
		a.subs[s] = struct{}{}
	}
	a.mu.Unlock()
	return replay, s.ch, func() {
		a.mu.Lock()
		a.dropLocked(s)
		a.mu.Unlock()
	}
}

// Stop asks the agent to exit with /exit, then kills it after grace. It
// returns once the process is gone.
func (a *Agent) Stop(grace time.Duration) error {
	if a.isStopped() {
		return nil
	}
	// The writes can block (full input buffer), so they must not delay the
	// grace timer. Once the process is killed, waitLoop closes the PTY, which
	// releases a blocked write.
	go func() {
		if a.Inject([]byte("/exit")) != nil {
			return
		}
		// The screen was read a few ms before this point. A dialog that
		// opened in the gap would take the Enter as a choice, so the box
		// must show exactly "/exit" before Enter. Otherwise kill.
		if !a.exitTyped() {
			a.log.Warn("agent: /exit not shown in the input box, killing instead of typing Enter", "agent_id", a.id)
			a.Kill()
			return
		}
		a.Inject([]byte("\r"))
	}()
	select {
	case <-a.done:
		return nil
	case <-time.After(grace):
	}
	return a.Kill()
}

// exitTyped polls until the input box shows exactly "/exit". It gives up
// early when the trust dialog is on screen.
func (a *Agent) exitTyped() bool {
	for i := 0; i < exitConfirmPolls; i++ {
		st := a.screen.State()
		if st.Trust {
			return false
		}
		if st.InputVisible && st.Draft == "/exit" {
			return true
		}
		time.Sleep(exitConfirmEvery)
	}
	return false
}

// Kill ends the process tree at once without typing anything, and returns
// once the process is gone. Use it when /exit could join a draft.
func (a *Agent) Kill() error {
	if a.isStopped() {
		return nil
	}
	var err error
	if p := a.cmd.Process; p != nil {
		if err = killTree(p); errors.Is(err, os.ErrProcessDone) {
			err = nil
		}
	}
	<-a.done
	return err
}
