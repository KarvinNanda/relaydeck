// Package delivery decides when it is safe to type the fixed nudge into a
// target agent so it calls read_messages. The message body is never typed.
//
// A nudged message stays unread until the agent calls read_messages. Once its
// nudge is confirmed and the agent is idle again with an empty input box, the
// loop re-nudges it at most once per message, so an agent that ignores it
// cannot cause a loop. A restart or a swallowed nudge requeues it, and a
// queued message is nudged as soon as the hooks say idle and the screen shows
// an empty input box with no overlay or dialog, but the one re-nudge is never
// given back. A DeliverNow request from the user always includes nudged
// messages and skips the draft check once.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
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
	// maxNudges is how many times one message is nudged when the nudge keeps
	// being swallowed (no UserPromptSubmit). After that it stays nudged.
	maxNudges = 3
	// stuckStatusAfter is how long a live process may show the hook status
	// stopped or starting before the screen decides instead.
	stuckStatusAfter = 15 * time.Second
	// externalTTL is how long a "session open elsewhere" answer is reused.
	externalTTL = 5 * time.Second
	// ctrlU clears the input line.
	ctrlU = "\x15"
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

// extResult is a cached ExternalOpen answer that held the messages.
type extResult struct {
	at time.Time
}

type nudge struct {
	at  time.Time
	ids []string
}

// Deliverer moves pending messages toward their targets. Delivery state is
// guarded by mu, so Tick may be called from several goroutines. Tick holds mu
// for a long time (process checks, paste polling), so prompt confirmations
// from the hook handler and DeliverNow requests from the UI go through the
// small pmu instead, and tickTarget takes them from there. Lock order is
// always mu then pmu.
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
	deliverNow map[Target]time.Time       // DeliverNow asks taken from pmu, by ask time
	noStart    map[Target]time.Time       // do not call Start before this time
	extWarned  map[Target]time.Time       // last ExternalOpen warning
	renudged   map[Target]map[string]bool // message ids that used their one re-nudge
	swallowed  map[Target]map[string]int  // swallowed nudges per message id
	lastNudge  map[Target]string          // text of the last nudge typed, to spot a stranded one
	stuck      map[Target]time.Time       // since when the hook status reads stopped or starting
	extCache   map[Target]extResult       // last "held" answer of ExternalOpen

	pmu     sync.Mutex
	prompts map[Target]time.Time // when UserPromptSubmit last fired, not yet seen by Tick
	asks    map[Target]time.Time // when DeliverNow was asked, not yet seen by Tick
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
		swallowed:  map[Target]map[string]int{},
		lastNudge:  map[Target]string{},
		stuck:      map[Target]time.Time{},
		extCache:   map[Target]extResult{},
		prompts:    map[Target]time.Time{},
		asks:       map[Target]time.Time{},
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
		d.safeTick(ctx)
	}
}

// safeTick runs one Tick. A panic (a bad screen state, a bug in Agents) is
// logged with its stack and the loop keeps running. Tick's own deferred
// Unlock runs while the panic unwinds, so the next Tick can take mu.
func (d *Deliverer) safeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("delivery: tick panic", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	d.Tick(ctx)
}

// PromptSubmitted records that the hook UserPromptSubmit fired. The next Tick
// uses it to confirm a waiting nudge. It never waits for a running Tick.
func (d *Deliverer) PromptSubmitted(ws, folder string) {
	d.pmu.Lock()
	d.prompts[Target{ws, folder}] = d.now()
	d.pmu.Unlock()
	d.Kick()
}

// DeliverNow makes the next delivery to this target skip the draft check once
// and include nudged messages. It never waits for a running Tick.
func (d *Deliverer) DeliverNow(ws, folder string) {
	d.pmu.Lock()
	d.asks[Target{ws, folder}] = d.now()
	d.pmu.Unlock()
	d.Kick()
}

// Tick checks every target once.
func (d *Deliverer) Tick(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	seen := map[Target]bool{}
	listed := map[Target]bool{}
	for _, t := range d.targets() {
		listed[t] = true
		if seen[t] || ctx.Err() != nil {
			continue
		}
		seen[t] = true
		d.tickTarget(t)
	}
	d.prune(listed)
}

// prune drops state for targets that targets() no longer lists, so the maps
// do not grow with every folder that was ever used.
func (d *Deliverer) prune(listed map[Target]bool) {
	d.pmu.Lock()
	for t := range d.asks {
		if !listed[t] {
			delete(d.asks, t)
		}
	}
	for t := range d.prompts {
		if !listed[t] {
			delete(d.prompts, t)
		}
	}
	d.pmu.Unlock()
	for t := range d.lastNudge {
		if !listed[t] {
			delete(d.lastNudge, t)
		}
	}
	for t := range d.stuck {
		if !listed[t] {
			delete(d.stuck, t)
		}
	}
	for t := range d.extCache {
		if !listed[t] {
			delete(d.extCache, t)
		}
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
	if at, ok := d.asks[t]; ok {
		d.deliverNow[t] = at // the TTL runs from the ask, not from this tick
		delete(d.asks, t)
	}
	d.pmu.Unlock()
	if n, ok := d.nudges[t]; ok && prompted && !promptAt.Before(n.at) {
		// The prompt came after the nudge time: the nudge was submitted. An
		// older prompt (a late hook for something else) confirms nothing.
		delete(d.nudges, t)
	}

	if n, ok := d.nudges[t]; ok && !d.now().Before(n.at.Add(confirmWindow)) {
		// No UserPromptSubmit: the nudge was swallowed. Queue it again, but
		// only until maxNudges nudges were swallowed for it. After that it
		// stays nudged (the agent was told) and only DeliverNow nudges it.
		delete(d.nudges, t)
		for _, m := range d.r.Pending(t.WS, t.Folder) {
			for _, id := range n.ids {
				if m.ID == id && m.State == notify.StateNudged {
					if d.swallowed[t] == nil {
						d.swallowed[t] = map[string]int{}
					}
					d.swallowed[t][id]++
					if d.swallowed[t][id] < maxNudges {
						d.set(m, notify.StateQueued)
					} else {
						d.log.Warn("delivery: nudge swallowed too often, not nudging again", "ws", t.WS, "agent", t.Folder, "message_id", id)
					}
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
		delete(d.swallowed, t)
		delete(d.lastNudge, t)
		delete(d.stuck, t)
		return
	}
	d.pruneRenudged(t, pending)

	if !d.a.Running(t.WS, t.Folder) {
		d.notRunning(t, pending)
		return
	}
	delete(d.extCache, t) // running here: a later stop is checked afresh

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
			if forced || (!d.renudged[t][m.ID] && d.swallowed[t][m.ID] < maxNudges) {
				cand = append(cand, m)
			}
		}
	}
	if len(cand) == 0 {
		delete(d.stuck, t)
		return
	}
	if waiting {
		delete(d.stuck, t)
		return // a nudge awaits its prompt confirmation: one nudge per idle cycle
	}

	switch d.effectiveStatus(t) {
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
	if draft && forced && scr.Draft != "" && scr.Draft == d.lastNudge[t] {
		// Our own nudge is stranded in the box (its Enter was swallowed). A
		// second nudge pasted after it would read as one garbled prompt, so
		// clear the box first.
		var ok bool
		if scr, ok = d.clearBox(t); !ok {
			return
		}
		draft = !scr.InputEmpty
	}
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

// effectiveStatus is the hook status, except that a live process stuck at
// stopped or starting for stuckStatusAfter reads as idle, so the screen
// decides. The hooks can leave that status behind (a SessionEnd handled after
// a SessionStart on /clear, a lost SessionStart). Busy and attention are
// never overridden.
func (d *Deliverer) effectiveStatus(t Target) agent.Status {
	st := d.a.Status(t.WS, t.Folder)
	if st != agent.StatusStopped && st != agent.StatusStarting {
		delete(d.stuck, t)
		return st
	}
	since, ok := d.stuck[t]
	if !ok {
		d.stuck[t] = d.now()
		return st
	}
	if !d.now().Before(since.Add(stuckStatusAfter)) {
		return agent.StatusIdle
	}
	return st
}

// clearBox sends Ctrl-U and waits for the box to show empty. It returns the
// screen and whether the box is empty and ready.
func (d *Deliverer) clearBox(t Target) (screen.State, bool) {
	if err := d.a.Inject(t.WS, t.Folder, []byte(ctrlU)); err != nil {
		d.log.Error("delivery: inject ctrl-u failed", "ws", t.WS, "agent", t.Folder, "err", err)
		return screen.State{}, false
	}
	d.sleep(d.enterDelay)
	var scr screen.State
	for i := 0; i < pollMax; i++ {
		scr = d.a.Screen(t.WS, t.Folder)
		if scr.InputVisible && scr.InputEmpty && !scr.Busy && !scr.Trust {
			return scr, true
		}
		d.sleep(pollEvery)
	}
	d.log.Warn("delivery: stranded nudge not cleared, not nudging again", "ws", t.WS, "agent", t.Folder)
	return scr, false
}

// pruneRenudged forgets re-nudge marks for messages that are no longer pending.
func (d *Deliverer) pruneRenudged(t Target, pending []notify.Message) {
	live := make(map[string]bool, len(pending))
	for _, m := range pending {
		live[m.ID] = true
	}
	for id := range d.renudged[t] {
		if !live[id] {
			delete(d.renudged[t], id)
		}
	}
	for id := range d.swallowed[t] {
		if !live[id] {
			delete(d.swallowed[t], id)
		}
	}
}

func (d *Deliverer) notRunning(t Target, pending []notify.Message) {
	delete(d.stuck, t)
	open := d.externalOpen(t)
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
	delete(d.swallowed, t) // a fresh process gets its full set of nudges
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

// externalOpen reports whether the folder's session is open outside
// relaydeck. The check lists agents and scans every process, so a "held"
// answer (open, or the check failed) is reused for externalTTL. A "not open"
// answer is never reused: a start follows it at once.
func (d *Deliverer) externalOpen(t Target) bool {
	if c, ok := d.extCache[t]; ok && d.now().Before(c.at.Add(externalTTL)) {
		return true
	}
	open, err := d.a.ExternalOpen(t.WS, t.Folder)
	if err != nil {
		if last, ok := d.extWarned[t]; !ok || !d.now().Before(last.Add(warnEvery)) {
			d.extWarned[t] = d.now()
			d.log.Warn("delivery: external session check failed, holding", "ws", t.WS, "agent", t.Folder, "err", err)
		}
		open = true
	}
	if open {
		d.extCache[t] = extResult{at: d.now()}
	} else {
		delete(d.extCache, t)
	}
	return open
}

// sendNudge pastes the nudge, waits for the paste to show on screen, then
// sends Enter. The text counts every unread message; cand are the messages the
// nudge is sent for. It reports whether Enter was sent.
func (d *Deliverer) sendNudge(t Target, cand, unread []notify.Message) bool {
	nudgeText := notify.Nudge(unread)
	text := "\x1b[200~" + nudgeText + "\x1b[201~"
	if err := d.a.Inject(t.WS, t.Folder, []byte(text)); err != nil {
		d.log.Error("delivery: inject failed", "ws", t.WS, "agent", t.Folder, "err", err)
		return false
	}
	d.lastNudge[t] = nudgeText
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
