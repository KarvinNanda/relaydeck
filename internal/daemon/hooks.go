package daemon

import (
	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/hook"
)

// onHook handles one hook report (ruling 5). The hook client gives up after
// 2 s, so this stays quick: it only sets state and calls Kick, never Tick.
// The identity comes from the agent token (auth.AgentID), never the body.
func (d *Daemon) onHook(agentID string, r hook.Report) {
	t, ok := d.reg.owner(agentID)
	if !ok {
		return
	}
	st, ok := hook.StatusFor(r)
	if !ok {
		return
	}
	if r.Event == "Stop" || r.Event == "UserPromptSubmit" {
		// The turn that read the messages ended, or the agent moved on to a
		// new turn: the messages it read are final.
		d.router.Confirm(agentID)
	}
	if r.Event == "UserPromptSubmit" {
		// Busy first, so a Tick between the two calls cannot see "idle"
		// and nudge again while the prompt that confirms the nudge runs.
		d.mgr.SetStatus(agentID, agent.StatusBusy)
		d.deliv.PromptSubmitted(t.WS, t.Folder)
	} else {
		d.mgr.SetStatus(agentID, agent.Status(st))
	}
	if r.Event == "SessionStart" {
		// /clear and /resume inside the agent move it to another session.
		d.setSessionID(t.WS, t.Folder, r.SessionID)
	}
	d.deliv.Kick()
}
