package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/notify"
	"github.com/KarvinNanda/relaydeck/internal/screen"
)

type dir map[string][]string

func (d dir) Folders(ws string) []string { return d[ws] }

type fakeAgents struct {
	mu          sync.Mutex
	running     bool
	status      agent.Status
	scr         screen.State
	pasted      bool
	scrSeq      []screen.State // consumed first, one per Screen call
	external    bool
	externalErr error
	startErr    error
	injectErr   error
	starts      int
	injected    [][]byte
}

func (f *fakeAgents) Running(ws, folder string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}
func (f *fakeAgents) Status(ws, folder string) agent.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}
func (f *fakeAgents) Screen(ws, folder string) screen.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.scrSeq) > 0 {
		s := f.scrSeq[0]
		f.scrSeq = f.scrSeq[1:]
		return s
	}
	if f.pasted {
		s := f.scr
		s.InputEmpty = false // the box holds the paste until Enter
		return s
	}
	return f.scr
}
func (f *fakeAgents) Start(ws, folder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return f.startErr
}
func (f *fakeAgents) ExternalOpen(ws, folder string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.external, f.externalErr
}
func (f *fakeAgents) Inject(ws, folder string, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.injectErr != nil {
		return f.injectErr
	}
	f.pasted = string(p) != "\r"
	f.injected = append(f.injected, append([]byte(nil), p...))
	return nil
}

func (f *fakeAgents) set(fn func(f *fakeAgents)) { f.mu.Lock(); defer f.mu.Unlock(); fn(f) }
func (f *fakeAgents) nInjected() int             { f.mu.Lock(); defer f.mu.Unlock(); return len(f.injected) }

var readyScreen = screen.State{InputVisible: true, InputEmpty: true}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	r  *notify.Router
	fa *fakeAgents
	d  *Deliverer
	c  *clock
}

func newRig() *rig {
	n := 0
	c := &clock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	r := notify.NewRouter(dir{"ws": {"be", "fe"}}, c.now, func() string { n++; return fmt.Sprintf("m%d", n) })
	fa := &fakeAgents{running: true, status: agent.StatusIdle, scr: readyScreen}
	d, err := New(r, fa, 0, c.now, func() []Target { return []Target{{"ws", "fe"}} })
	if err != nil {
		panic(err)
	}
	d.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return &rig{r, fa, d, c}
}

func (g *rig) send(t *testing.T, body string) notify.Message {
	t.Helper()
	m, err := g.r.Send("ws", "be", "fe", body)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (g *rig) state(id string) notify.State {
	for _, m := range g.r.Timeline("ws") {
		if m.ID == id {
			return m.State
		}
	}
	return ""
}

func (g *rig) tick() { g.d.Tick(context.Background()) }

func TestIdleTargetGetsOneNudge(t *testing.T) {
	g := newRig()
	m := g.send(t, "secret body")
	g.tick()
	want := "\x1b[200~" + notify.Nudge([]notify.Message{m}) + "\x1b[201~"
	if g.fa.nInjected() != 2 || string(g.fa.injected[0]) != want || string(g.fa.injected[1]) != "\r" {
		t.Fatalf("injected %q", g.fa.injected)
	}
	if got := g.state(m.ID); got != notify.StateNudged {
		t.Fatalf("state %s", got)
	}
	if strings.Contains(string(g.fa.injected[0]), "secret body") {
		t.Fatal("body typed")
	}
}

func TestBusyTargetWaits(t *testing.T) {
	for _, s := range []agent.Status{agent.StatusBusy, agent.StatusAttention, agent.StatusStarting} {
		g := newRig()
		m := g.send(t, "x")
		g.fa.set(func(f *fakeAgents) { f.status = s })
		g.tick()
		if g.fa.nInjected() != 0 || g.state(m.ID) != notify.StateQueued {
			t.Fatalf("%s: injected or moved", s)
		}
	}
}

func TestStoppedTargetIsStarted(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.running = false })
	g.tick()
	if g.fa.starts != 1 || g.fa.nInjected() != 0 || g.state(m.ID) != notify.StateQueued {
		t.Fatalf("starts %d", g.fa.starts)
	}
}

func TestExternalSessionHolds(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.running = false; f.external = true })
	g.tick()
	if g.fa.starts != 0 || g.state(m.ID) != notify.StateHeldExternal {
		t.Fatalf("starts %d state %s", g.fa.starts, g.state(m.ID))
	}
}

func TestExternalOpenErrorHolds(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.running = false; f.externalErr = errors.New("ps failed") })
	g.tick()
	if g.fa.starts != 0 || g.state(m.ID) != notify.StateHeldExternal {
		t.Fatalf("starts %d state %s", g.fa.starts, g.state(m.ID))
	}
}

func TestHeldExternalReleasedWhenClosed(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.running = false; f.external = true })
	g.tick()
	g.fa.set(func(f *fakeAgents) { f.external = false })
	g.tick()
	if g.fa.starts != 1 || g.state(m.ID) != notify.StateQueued {
		t.Fatalf("starts %d state %s", g.fa.starts, g.state(m.ID))
	}
}

func TestStartBackoff(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.running = false; f.startErr = errors.New("boom") })
	g.tick()
	g.c.add(5 * time.Second)
	g.tick()
	if g.fa.starts != 1 {
		t.Fatalf("starts %d, want 1 inside backoff", g.fa.starts)
	}
	g.c.add(6 * time.Second)
	g.tick()
	if g.fa.starts != 2 || g.state(m.ID) != notify.StateQueued {
		t.Fatalf("starts %d state %s", g.fa.starts, g.state(m.ID))
	}
}

func TestDraftHolds(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.scr = screen.State{InputVisible: true, InputEmpty: false} })
	g.tick()
	if g.fa.nInjected() != 0 || g.state(m.ID) != notify.StateHeldDraft {
		t.Fatalf("state %s", g.state(m.ID))
	}
	g.fa.set(func(f *fakeAgents) { f.scr = readyScreen })
	g.tick()
	if g.fa.nInjected() != 2 || g.state(m.ID) != notify.StateNudged {
		t.Fatalf("state %s", g.state(m.ID))
	}
}

func TestOverlayWaits(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.scr = screen.State{InputVisible: false} })
	g.tick()
	if g.fa.nInjected() != 0 || g.state(m.ID) != notify.StateQueued {
		t.Fatal("must wait")
	}
}

func TestTrustDialogWaits(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.scr = screen.State{InputVisible: true, InputEmpty: true, Trust: true} })
	g.tick()
	if g.fa.nInjected() != 0 || g.state(m.ID) != notify.StateQueued {
		t.Fatal("must wait")
	}
}

func TestSwallowedNudgeRequeued(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.tick()
	g.c.add(2 * time.Second)
	g.tick()
	if g.state(m.ID) != notify.StateNudged {
		t.Fatal("too early")
	}
	g.c.add(2 * time.Second)
	g.tick()
	if g.state(m.ID) != notify.StateNudged || g.fa.nInjected() != 4 {
		// requeued and nudged again in the same tick, status never left idle
		t.Fatalf("state %s injected %d", g.state(m.ID), g.fa.nInjected())
	}
	if g.fa.nInjected() != 4 || g.state(m.ID) != notify.StateNudged {
		t.Fatalf("injected %d state %s", g.fa.nInjected(), g.state(m.ID))
	}
}

func TestPromptSubmittedKeepsNudged(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.tick()
	g.d.PromptSubmitted("ws", "fe")
	g.c.add(10 * time.Second)
	g.tick()
	if g.state(m.ID) != notify.StateNudged || g.fa.nInjected() != 2 {
		t.Fatalf("state %s injected %d", g.state(m.ID), g.fa.nInjected())
	}
}

func TestNudgedNotRenudged(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.tick()
	g.d.PromptSubmitted("ws", "fe")
	g.tick()
	g.tick()
	if g.fa.nInjected() != 2 || g.state(m.ID) != notify.StateNudged {
		t.Fatalf("injected %d", g.fa.nInjected())
	}
}

func TestDeliverNowSkipsDraftOnce(t *testing.T) {
	g := newRig()
	g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.scr = screen.State{InputVisible: true, InputEmpty: false} })
	g.tick()
	g.d.DeliverNow("ws", "fe")
	g.tick()
	if g.fa.nInjected() != 2 {
		t.Fatalf("injected %d", g.fa.nInjected())
	}
	// Second message, draft still there: held again (the ask was used up).
	m2 := g.send(t, "y")
	g.d.PromptSubmitted("ws", "fe")
	g.fa.set(func(f *fakeAgents) { f.status = agent.StatusIdle })
	g.tick()
	if g.fa.nInjected() != 2 || g.state(m2.ID) != notify.StateHeldDraft {
		t.Fatalf("injected %d state %s", g.fa.nInjected(), g.state(m2.ID))
	}
}

func TestAwaitingApprovalNotDelivered(t *testing.T) {
	g := newRig()
	// Build a chain be->fe, fe->be, be->fe: the third message reaches the hop limit.
	g.send(t, "a")
	g.r.Read("ws", "fe")
	if _, err := g.r.Send("ws", "fe", "be", "b"); err != nil {
		t.Fatal(err)
	}
	g.r.Read("ws", "be")
	m := g.send(t, "c")
	if m.State != notify.StateAwaitingApproval {
		t.Fatalf("state %s", m.State)
	}
	g.tick()
	if g.fa.nInjected() != 0 {
		t.Fatal("awaiting approval must not be nudged")
	}
	if err := g.r.Approve(m.ID); err != nil {
		t.Fatal(err)
	}
	g.tick()
	if g.fa.nInjected() != 2 || g.state(m.ID) != notify.StateNudged {
		t.Fatalf("state %s", g.state(m.ID))
	}
}

func TestTargetExitsWhileQueued(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.status = agent.StatusBusy })
	g.tick()
	g.fa.set(func(f *fakeAgents) { f.running = false; f.status = agent.StatusStopped })
	g.tick()
	if g.state(m.ID) != notify.StateQueued || g.fa.starts != 1 {
		t.Fatalf("state %s starts %d", g.state(m.ID), g.fa.starts)
	}
}

func TestTwoMessagesOneNudge(t *testing.T) {
	g := newRig()
	a := g.send(t, "x")
	b := g.send(t, "y")
	g.tick()
	if g.fa.nInjected() != 2 {
		t.Fatalf("injected %d", g.fa.nInjected())
	}
	if g.state(a.ID) != notify.StateNudged || g.state(b.ID) != notify.StateNudged {
		t.Fatal("both must be nudged")
	}
}

func TestInjectErrorKeepsQueued(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.injectErr = errors.New("pty closed") })
	g.tick()
	if g.state(m.ID) != notify.StateQueued {
		t.Fatalf("state %s", g.state(m.ID))
	}
}

func TestRunTicksOnKick(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.d.Run(ctx); close(done) }()
	g.d.Kick()
	g.d.Kick()
	deadline := time.Now().Add(2 * time.Second)
	for g.state(m.ID) != notify.StateNudged && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if g.state(m.ID) != notify.StateNudged {
		t.Fatal("Run did not deliver")
	}
}

func TestNudgedTargetCrashesRequeued(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.tick()
	g.d.PromptSubmitted("ws", "fe")
	g.fa.set(func(f *fakeAgents) { f.running = false; f.status = agent.StatusStopped })
	g.tick()
	if g.state(m.ID) != notify.StateQueued || g.fa.starts != 1 {
		t.Fatalf("state %s starts %d", g.state(m.ID), g.fa.starts)
	}
	g.fa.set(func(f *fakeAgents) { f.running = true; f.status = agent.StatusIdle })
	g.tick()
	if g.state(m.ID) != notify.StateNudged || g.fa.nInjected() != 4 {
		t.Fatalf("state %s injected %d", g.state(m.ID), g.fa.nInjected())
	}
}

func TestDeliverNowBusyScreenWaits(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.scr = screen.State{InputVisible: true, InputEmpty: false, Busy: true} })
	g.d.DeliverNow("ws", "fe")
	g.tick()
	if g.fa.nInjected() != 0 || g.state(m.ID) != notify.StateQueued {
		t.Fatalf("injected %d state %s", g.fa.nInjected(), g.state(m.ID))
	}
}

func TestDeliverNowExpires(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) { f.status = agent.StatusBusy })
	g.d.DeliverNow("ws", "fe")
	g.tick()
	g.c.add(6 * time.Second)
	g.fa.set(func(f *fakeAgents) {
		f.status = agent.StatusIdle
		f.scr = screen.State{InputVisible: true, InputEmpty: false}
	})
	g.tick()
	if g.fa.nInjected() != 0 || g.state(m.ID) != notify.StateHeldDraft {
		t.Fatalf("injected %d state %s", g.fa.nInjected(), g.state(m.ID))
	}
}

func TestEnterSkippedWhenScreenChanges(t *testing.T) {
	cases := map[string]screen.State{
		"overlay": {InputVisible: false},
		"trust":   {InputVisible: true, InputEmpty: false, Trust: true},
		"busy":    {InputVisible: true, InputEmpty: false, Busy: true},
		"emptied": {InputVisible: true, InputEmpty: true},
	}
	for name, after := range cases {
		g := newRig()
		m := g.send(t, "x")
		g.fa.set(func(f *fakeAgents) { f.scrSeq = []screen.State{readyScreen, after} })
		g.tick()
		if g.fa.nInjected() != 1 || g.state(m.ID) != notify.StateQueued {
			t.Fatalf("%s: injected %d state %s", name, g.fa.nInjected(), g.state(m.ID))
		}
	}
}

func TestEnterSentWhenBoxHoldsPaste(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	g.fa.set(func(f *fakeAgents) {
		f.scrSeq = []screen.State{readyScreen, {InputVisible: true, InputEmpty: false}}
	})
	g.tick()
	if g.fa.nInjected() != 2 || g.state(m.ID) != notify.StateNudged {
		t.Fatalf("injected %d state %s", g.fa.nInjected(), g.state(m.ID))
	}
}

// readerAgents reads the messages while the paste is injected, like an agent
// that calls read_messages by itself.
type readerAgents struct {
	*fakeAgents
	r *notify.Router
}

func (a readerAgents) Inject(ws, folder string, p []byte) error {
	a.r.Read(ws, folder)
	return a.fakeAgents.Inject(ws, folder, p)
}

func TestSetStateErrorSkipped(t *testing.T) {
	g := newRig()
	m := g.send(t, "x")
	d, err := New(g.r, readerAgents{g.fa, g.r}, 0, g.c.now, func() []Target { return []Target{{"ws", "fe"}} })
	if err != nil {
		t.Fatal(err)
	}
	d.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	g.fa.set(func(f *fakeAgents) {
		f.scrSeq = []screen.State{readyScreen, {InputVisible: true, InputEmpty: false}}
	})
	d.Tick(context.Background())
	if g.state(m.ID) != notify.StateDelivered {
		t.Fatalf("state %s", g.state(m.ID))
	}
	d.Tick(context.Background())
	if g.fa.nInjected() != 2 {
		t.Fatalf("injected %d", g.fa.nInjected())
	}
}

func TestNewRequiresTargets(t *testing.T) {
	if _, err := New(nil, nil, 0, time.Now, nil); err == nil {
		t.Fatal("nil targets must fail")
	}
}
