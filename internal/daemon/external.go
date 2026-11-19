package daemon

import (
	"context"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/delivery"
)

// pollExternal checks every ExternalEvery whether a running agent's folder or
// session is also open in a claude outside relaydeck (#89). The two processes
// do not sync: the panel stays at its last state and typing in both splits
// the session history, so the panel shows a warning. Delivery holds such an
// agent's messages anyway (its own check); this only makes it visible.
func (d *Daemon) pollExternal(ctx context.Context) {
	t := time.NewTicker(d.opts.ExternalEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.refreshExternal(ctx)
	}
}

type runningAgent struct {
	t       delivery.Target
	id      string
	dir     string
	session string
}

func (d *Daemon) refreshExternal(ctx context.Context) {
	var agents []runningAgent
	for _, t := range d.targets() {
		a := d.running(t)
		if a == nil {
			continue
		}
		_, f, err := d.folderSnapshot(t.WS, t.Folder)
		if err != nil {
			continue
		}
		agents = append(agents, runningAgent{t: t, id: a.ID(), dir: f.Path, session: f.SessionID})
	}
	next := map[string]int{}
	if len(agents) > 0 {
		match, err := d.opts.ExternalList(ctx)
		if err != nil {
			// Unknown is not "open": the warning is only a hint, and a failed
			// read must not flash it. Keep the last answer.
			return
		}
		own := d.opts.OwnPIDs()
		for _, a := range agents {
			if pid, open := match(a.dir, a.session, own); open {
				next[a.id] = pid
			}
		}
	}
	d.extMu.Lock()
	prev := d.extPID
	d.extPID = next
	d.extMu.Unlock()
	for _, a := range agents {
		if prev[a.id] != next[a.id] {
			d.hub.publish(Event{Type: "external", WS: a.t.WS, Folder: a.t.Folder})
		}
	}
}

// externalPID is the pid of the outside claude for a running agent, or 0.
func (d *Daemon) externalPID(agentID string) int {
	d.extMu.Lock()
	defer d.extMu.Unlock()
	return d.extPID[agentID]
}
