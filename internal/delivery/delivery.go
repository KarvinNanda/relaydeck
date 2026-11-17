// Package delivery decides when it is safe to type the fixed nudge into a
// target agent so it calls read_messages. The message body is never typed.
package delivery

import (
	"context"
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
	MarkBusy(ws, folder string) // optimistic, until hooks report
}

// Target is one agent: a folder inside a workspace.
type Target struct{ WS, Folder string }

type nudge struct {
	at  time.Time
	ids []string
}

// Deliverer moves pending messages toward their targets. All state is guarded
// by one mutex, so Tick may be called from several goroutines.
type Deliverer struct {
	// Targets lists the agents to check on each Tick. The router has no
	// index of targets, so the wiring code supplies one.
	Targets func() []Target

	r          *notify.Router
	a          Agents
	enterDelay time.Duration
	now        func() time.Time
	log        *slog.Logger
	kick       chan struct{}

	mu         sync.Mutex
	nudges     map[Target]nudge
	deliverNow map[Target]bool
	noStart    map[Target]time.Time // do not call Start before this time
}

func New(r *notify.Router, a Agents, enterDelay time.Duration, now func() time.Time) *Deliverer {
	return &Deliverer{
		r: r, a: a, enterDelay: enterDelay, now: now,
		log:        slog.Default(),
		kick:       make(chan struct{}, 1),
		nudges:     map[Target]nudge{},
		deliverNow: map[Target]bool{},
		noStart:    map[Target]time.Time{},
	}
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

// PromptSubmitted confirms a nudge: the hook UserPromptSubmit fired.
func (d *Deliverer) PromptSubmitted(ws, folder string) {
	d.mu.Lock()
	delete(d.nudges, Target{ws, folder})
	d.mu.Unlock()
}

// DeliverNow makes the next delivery to this target skip the draft check once.
func (d *Deliverer) DeliverNow(ws, folder string) {
	d.mu.Lock()
	d.deliverNow[Target{ws, folder}] = true
	d.mu.Unlock()
	d.Kick()
}

// Tick checks every target once.
func (d *Deliverer) Tick(ctx context.Context) {
	if d.Targets == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	seen := map[Target]bool{}
	for _, t := range d.Targets() {
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
		d.log.Debug("delivery: message moved elsewhere", "message_id", m.ID, "err", err)
	}
}

func (d *Deliverer) tickTarget(t Target) {
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
		return
	}

	if !d.a.Running(t.WS, t.Folder) {
		d.notRunning(t, pending)
		return
	}

	// Running here means relaydeck owns the session, so external holds are over.
	var cand []notify.Message
	for _, m := range pending {
		switch m.State {
		case notify.StateHeldExternal:
			d.set(m, notify.StateQueued)
			m.State = notify.StateQueued
			cand = append(cand, m)
		case notify.StateQueued, notify.StateHeldDraft:
			cand = append(cand, m)
		}
	}
	if len(cand) == 0 {
		return // only nudged messages: the agent was told, never nudge again
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
	draft := !scr.InputEmpty
	if draft && !d.deliverNow[t] {
		for _, m := range cand {
			d.set(m, notify.StateHeldDraft)
		}
		return
	}
	if !draft && !scr.Ready() {
		return
	}
	d.sendNudge(t, cand)
	delete(d.deliverNow, t)
}

func (d *Deliverer) notRunning(t Target, pending []notify.Message) {
	open, err := d.a.ExternalOpen(t.WS, t.Folder)
	if err != nil {
		d.log.Warn("delivery: external session check failed, holding", "folder", t.Folder, "err", err)
		open = true
	}
	if open {
		for _, m := range pending {
			if m.State != notify.StateHeldExternal && m.State != notify.StateAwaitingApproval {
				d.set(m, notify.StateHeldExternal)
			}
		}
		return
	}
	for _, m := range pending {
		if m.State == notify.StateHeldExternal {
			d.set(m, notify.StateQueued)
		}
	}
	if until, ok := d.noStart[t]; ok && d.now().Before(until) {
		return
	}
	if err := d.a.Start(t.WS, t.Folder); err != nil {
		d.noStart[t] = d.now().Add(startBackoff)
		d.log.Error("delivery: start failed", "folder", t.Folder, "err", err)
		return
	}
	delete(d.noStart, t)
}

func (d *Deliverer) sendNudge(t Target, cand []notify.Message) {
	text := "\x1b[200~" + notify.Nudge(cand) + "\x1b[201~"
	if err := d.a.Inject(t.WS, t.Folder, []byte(text)); err != nil {
		d.log.Error("delivery: inject failed", "folder", t.Folder, "err", err)
		return
	}
	time.Sleep(d.enterDelay)
	if err := d.a.Inject(t.WS, t.Folder, []byte("\r")); err != nil {
		d.log.Error("delivery: inject enter failed", "folder", t.Folder, "err", err)
		return
	}
	ids := make([]string, 0, len(cand))
	for _, m := range cand {
		d.set(m, notify.StateNudged)
		ids = append(ids, m.ID)
	}
	d.a.MarkBusy(t.WS, t.Folder)
	d.nudges[t] = nudge{at: d.now(), ids: ids}
}
