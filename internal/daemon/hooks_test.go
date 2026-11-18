package daemon

import (
	"testing"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/hook"
)

// Ruling 5 (moved here from Task 9): SessionStart with a new session id
// updates the folder's SessionID in config and on disk.
func TestSessionStartUpdatesSessionID(t *testing.T) {
	e := newEnv(t, nil)
	id, _, err := e.d.reg.issue(delivery.Target{WS: "w", Folder: "b"})
	if err != nil {
		t.Fatal(err)
	}
	const sid = "55555555-5555-4555-8555-555555555555"
	e.d.onHook(id, hook.Report{Event: "SessionStart", SessionID: sid})
	if got := e.savedFolder(t, "b").SessionID; got != sid {
		t.Fatalf("saved session id = %q, want %q", got, sid)
	}
	// An empty id (dropped by the handler as invalid) changes nothing.
	e.d.onHook(id, hook.Report{Event: "SessionStart"})
	if got := e.savedFolder(t, "b").SessionID; got != sid {
		t.Fatalf("empty id overwrote session id: %q", got)
	}
	// Other events never touch the session id.
	e.d.onHook(id, hook.Report{Event: "Stop", SessionID: "66666666-6666-4666-8666-666666666666"})
	if got := e.savedFolder(t, "b").SessionID; got != sid {
		t.Fatalf("Stop changed session id: %q", got)
	}
	// Unknown agent ids are ignored.
	e.d.onHook("nobody", hook.Report{Event: "SessionStart", SessionID: "77777777-7777-4777-8777-777777777777"})
	if got := e.savedFolder(t, "a").SessionID; got != "" {
		t.Fatalf("unknown agent changed folder a: %q", got)
	}
}

// Ruling 5: hook events set the agent status through hook.StatusFor.
func TestHookEventsSetStatus(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id := e.agentID(t, "a")
	a, _ := e.d.Agent("w", "a")
	if a.Status() != agent.StatusStarting {
		t.Fatalf("initial status %s", a.Status())
	}
	steps := []struct {
		r    hook.Report
		want agent.Status
	}{
		{hook.Report{Event: "SessionStart"}, agent.StatusIdle},
		{hook.Report{Event: "UserPromptSubmit"}, agent.StatusBusy},
		{hook.Report{Event: "Stop"}, agent.StatusIdle},
		{hook.Report{Event: "Notification", NotificationType: "permission_prompt"}, agent.StatusAttention},
		{hook.Report{Event: "Notification", NotificationType: "idle_prompt"}, agent.StatusIdle},
	}
	for _, s := range steps {
		e.d.onHook(id, s.r)
		if got := a.Status(); got != s.want {
			t.Fatalf("after %+v status = %s, want %s", s.r, got, s.want)
		}
	}
}
