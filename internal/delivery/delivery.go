// Package delivery decides when it is safe to type the fixed nudge into a
// target agent so it calls read_messages. The message body is never typed.
//
// A nudged message stays unread until the agent calls read_messages. Once its
// nudge is confirmed and the agent is idle again with an empty input box, the
// loop nudges it one more time, and never again after that, so an agent that
// ignores it cannot cause a loop. A DeliverNow request from the user always
// includes nudged messages.
package delivery

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/notify"
	"github.com/KarvinNanda/relaydeck/internal/screen"
)

const (
	confirmWindow = 3 * time.Second
	startBackoff  = 10 * time.Second
	deliverNowTTL = 5 * time.Second
	warnEvery     = 30 * time.Second
	pollEvery     = 25 * time.Millisecond
	pollMax       = 20 // 500 ms
	tickEvery     = time.Second
)

// Agents is what the delivery loop needs from the agent manager.
type Agents interface {
	Running(ws, folder string) bool
	Status(ws, folder string) agent.Status // from hooks
	Screen(ws, folder string) screen.State
	Start(ws, folder string) error // resume current session or new
	ExternalOpen(ws, folder string) (bool, error)
	Inject(ws, folder string, p []byte) error
}

// Target is one agent: a folder inside a workspace.
type Target struct{ WS, Folder string }

type nudge struct {
	at  time.Time
	ids []string
}

// Deliverer moves pending messages toward their targets. Delivery state is
// guarded by mu, so Tick may be called from several goroutines. Tick holds mu
// for a long time (process checks, paste polling), so prompt confirmations
// from the hook handler go through the small pmu instead. Lock order is always
// mu then pmu.
type Deliverer struct {
	r          *notify.Router
	a          Agents
	enterDelay time.Duration
	now        func() time.Time
	sleep      func(time.Duration) // injected so tests stay fast
	log        *slog.Logger
	targets    func() []Target
	kick       chan struct{}

	mu         sync.Mutex
	nudges     map[Target]nudge
	deliverNow map[Target]time.Time       // when DeliverNow was asked
	noStart    map[Target]time.Time       // do not call Start before this time
	extWarned  map[Target]time.Time       // last ExternalOpen warning
	renudged   map[Target]map[string]bool // message ids that used their one re-nudge

	pmu     sync.Mutex
	prompts map[Target]time.Time // when UserPromptSubmit last fired, not yet seen by Tick
}

// New builds a Deliverer. targets lists the agents to check on each Tick (the
// router has no index of targets, so the caller supplies one). It must not be nil.
func New(r *notify.Router, a Agents, enterDelay time.Duration, now func() time.Time, targets func() []Target) (*Deliverer, error) {
	if targets == nil {
		return nil, errors.New("delivery: targets func is required")
	}
	return &Deliverer{
		r: r, a: a, enterDelay: enterDelay, now: now, sleep: time.Sleep, targets: targets,
		log:        slog.Default(),
		kick:       make(chan struct{}, 1),
		nudges:     map[Target]nudge{},
		deliverNow: map[Target]time.Time{},
		noStart:    map[Target]time.Time{},
		extWarned:  map[Target]time.Time{},
		renudged:   map[Target]map[string]bool{},
		prompts:    map[Target]time.Time{},
	}, nil
}

// Kick asks Run for an immediate Tick. It never blocks and coalesces.
func (d *Deliverer) Kick() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// Run calls Tick every second and on every Kick until ctx ends.
func (d *Deliverer) Run(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.kick:
		}
		d.Tick(ctx)
	}
}

// PromptSubmitted records that the hook UserPromptSubmit fired. The next Tick
// uses it to confirm a waiting nudge. It never waits for a running Tick.
func (d *Deliverer) PromptSubmitted(ws, folder string) {
	d.pmu.Lock()
	d.prompts[Target{ws, folder}] = d.now()
	d.pmu.Unlock()
	d.Kick()
}

// DeliverNow makes the next delivery to this target skip the draft check once.
func (d *Deliverer) DeliverNow(ws, folder string) {
	d.mu.Lock()
	d.deliverNow[Target{ws, folder}] = d.now()
	d.mu.Unlock()
	d.Kick()
}

// Tick checks every target once.
func (d *Deliverer) Tick(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	seen := map[Target]bool{}
	for _, t := range d.targets() {
		if seen[t] || ctx.Err() != nil {
			continue
		}
		seen[t] = true
		d.tickTarget(t)
	}
}

func (d *Deliverer) set(m notify.Message, s notify.State) {
	if m.State == s {
		return
	}
	if err := d.r.SetState(m.ID, s); err != nil {
		// Someone else moved it (read, approval, reject). Skip, never retry.
		d.log.Debug("delivery: message moved elsewhere", "ws", m.Workspace, "agent", m.To, "message_id", m.ID, "err", err)
	}
}

func (d *Deliverer) tickTarget(t Target) {
	d.pmu.Lock()
	promptAt, prompted := d.prompts[t]
	delete(d.prompts, t)
	d.pmu.Unlock()
	if n, ok := d.nudges[t]; ok && prompted && !promptAt.Before(n.at) {
		// The prompt came after the nudge time: the nudge was submitted. An
		// older prompt (a late hook for something else) confirms nothing.
		delete(d.nudges, t)
	}

	if n, ok := d.nudges[t]; ok && !d.now().Before(n.at.Add(confirmWindow)) {
		// No UserPromptSubmit: the nudge was swallowed. Queue it again.
		delete(d.nudges, t)
		for _, m := range d.r.Pending(t.WS, t.Folder) {
			for _, id := range n.ids {
				if m.ID == id && m.State == notify.StateNudged {
					d.set(m, notify.StateQueued)
				}
			}
		}
	}

	pending := d.r.Pending(t.WS, t.Folder)
	if len(pending) == 0 {
		delete(d.nudges, t)
		delete(d.deliverNow, t)
		delete(d.noStart, t)
		delete(d.renudged, t)
		return
	}
	d.pruneRenudged(t, pending)

	if !d.a.Running(t.WS, t.Folder) {
		d.notRunning(t, pending)
		return
	}

	asked, forced := d.deliverNow[t] // the user asked with DeliverNow
	if forced && !d.now().Before(asked.Add(deliverNowTTL)) {
		delete(d.deliverNow, t)
		forced = false
	}
	_, waiting := d.nudges[t]

	// Running here means relaydeck owns the session, so external holds are over.
	// unread is everything read_messages will return, so the nudge counts it
	// all. cand is what this nudge is sent for.
	var unread, cand []notify.Message
	for _, m := range pending {
		if m.State == notify.StateHeldExternal {
			d.set(m, notify.StateQueued)
			m.State = notify.StateQueued
		}
		unread = append(unread, m)
		switch m.State {
		case notify.StateQueued, notify.StateHeldDraft:
			cand = append(cand, m)
		case notify.StateNudged:
			// Told but still unread: one more nudge once its nudge is
			// confirmed (see waiting below), or any number when the user
			// asks with DeliverNow.
			if forced || !d.renudged[t][m.ID] {
				cand = append(cand, m)
			}
		}
	}
	if len(cand) == 0 {
		return
	}
	if waiting {
		return // a nudge awaits its prompt confirmation: one nudge per idle cycle
	}

	switch d.a.Status(t.WS, t.Folder) {
	case agent.StatusIdle:
	default:
		return // starting, busy, attention, stopped: wait
	}
	scr := d.a.Screen(t.WS, t.Folder)
	if scr.Trust || !scr.InputVisible {
		return // dialog or overlay: wait
	}
	if scr.Busy {
		return
	}
	draft := !scr.InputEmpty
	if draft && !forced {
		for _, m := range cand {
			if m.State != notify.StateNudged { // keep "the agent was told" visible
				d.set(m, notify.StateHeldDraft)
			}
		}
		return
	}
	if !draft && !scr.Ready() {
		return
	}
	if d.sendNudge(t, cand, unread) {
		delete(d.deliverNow, t)
	}
}

// pruneRenudged forgets re-nudge marks for messages that are no longer pending.
func (d *Deliverer) pruneRenudged(t Target, pending []notify.Message) {
	r := d.renudged[t]
	if len(r) == 0 {
		return
	}
	live := make(map[string]bool, len(pending))
	for _, m := range pending {
		live[m.ID] = true
	}
	for id := range r {
		if !live[id] {
			delete(r, id)
		}
	}
}

func (d *Deliverer) notRunning(t Target, pending []notify.Message) {
	open, err := d.a.ExternalOpen(t.WS, t.Folder)
	if err != nil {
		if last, ok := d.extWarned[t]; !ok || !d.now().Before(last.Add(warnEvery)) {
			d.extWarned[t] = d.now()
			d.log.Warn("delivery: external session check failed, holding", "ws", t.WS, "agent", t.Folder, "err", err)
		}
		open = true
	}
	if open {
		for _, m := range pending {
			if m.State != notify.StateHeldExternal {
				d.set(m, notify.StateHeldExternal)
			}
		}
		return
	}
	// The process that was nudged is gone, so a nudged message is unread
	// and must be nudged again after the restart.
	delete(d.nudges, t)
	for _, m := range pending {
		if m.State == notify.StateHeldExternal || m.State == notify.StateNudged {
			d.set(m, notify.StateQueued)
		}
	}
	if until, ok := d.noStart[t]; ok && d.now().Before(until) {
		return
	}
	if err := d.a.Start(t.WS, t.Folder); err != nil {
		d.noStart[t] = d.now().Add(startBackoff)
		d.log.Error("delivery: start failed", "ws", t.WS, "agent", t.Folder, "err", err)
		return
	}
	delete(d.noStart, t)
}

// sendNudge pastes the nudge, waits for the paste to show on screen, then
// sends Enter. The text counts every unread message; cand are the messages the
// nudge is sent for. It reports whether Enter was sent.
func (d *Deliverer) sendNudge(t Target, cand, unread []notify.Message) bool {
	text := "\x1b[200~" + notify.Nudge(unread) + "\x1b[201~"
	if err := d.a.Inject(t.WS, t.Folder, []byte(text)); err != nil {
		d.log.Error("delivery: inject failed", "ws", t.WS, "agent", t.Folder, "err", err)
		return false
	}
	d.sleep(d.enterDelay)
	// The PTY reader feeds the screen asynchronously, so poll until the box
	// shows the paste. If an overlay or dialog shows instead, or the paste never
	// appears, Enter could pick an option or submit nothing: leave the messages
	// queued. (This does not detect user typing during the wait.)
	ready := false
	for i := 0; i < pollMax; i++ {
		scr := d.a.Screen(t.WS, t.Folder)
		if scr.InputVisible && !scr.InputEmpty && !scr.Busy && !scr.Trust {
			ready = true
			break
		}
		d.sleep(pollEvery)
	}
	if !ready {
		d.log.Warn("delivery: paste not confirmed on screen, not sending Enter", "ws", t.WS, "agent", t.Folder)
		return false
	}
	// Take the nudge time before Enter: the hook can arrive within
	// milliseconds, and a prompt earlier than this time is not a confirmation.
	at := d.now()
	if err := d.a.Inject(t.WS, t.Folder, []byte("\r")); err != nil {
		d.log.Error("delivery: inject enter failed", "ws", t.WS, "agent", t.Folder, "err", err)
		return false
	}
	ids := make([]string, 0, len(cand))
	for _, m := range cand {
		if m.State == notify.StateNudged {
			if d.renudged[t] == nil {
				d.renudged[t] = map[string]bool{}
			}
			d.renudged[t][m.ID] = true
		}
		d.set(m, notify.StateNudged)
		ids = append(ids, m.ID)
	}
	d.nudges[t] = nudge{at: at, ids: ids}
	return true
}
