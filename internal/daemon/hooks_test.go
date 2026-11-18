package daemon

import (
	"testing"
	"time"

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

// #41: a message read in a turn that never reached Stop is queued again when
// the agent process exits. After the Stop hook it is final.
func TestReadMessagesRequeuedWhenAgentExitsBeforeStop(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.router.Send("w", "b", "a", "hello"); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.d.router.ReadAs("w", "a", e.agentID(t, "a"), 1<<20); len(got) != 1 {
		t.Fatalf("read %d", len(got))
	}
	a, _ := e.d.Agent("w", "a")
	if err := e.d.StopAgent("w", "a", true); err != nil {
		t.Fatal(err)
	}
	<-a.Done()
	eventually(t, "message queued again", func() bool { return len(e.d.router.Pending("w", "a")) == 1 })
}

func TestReadMessagesStayDeliveredAfterStopHook(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id := e.agentID(t, "a")
	if _, err := e.d.router.Send("w", "b", "a", "hello"); err != nil {
		t.Fatal(err)
	}
	e.d.router.ReadAs("w", "a", id, 1<<20)
	e.d.onHook(id, hook.Report{Event: "Stop"})
	a, _ := e.d.Agent("w", "a")
	if err := e.d.StopAgent("w", "a", true); err != nil {
		t.Fatal(err)
	}
	<-a.Done()
	// watch runs Requeue right after Done; give it time, then check nothing came back.
	time.Sleep(200 * time.Millisecond)
	if n := len(e.d.router.Pending("w", "a")); n != 0 {
		t.Fatalf("%d messages requeued after Stop", n)
	}
}

// Enter does not clear the "user touched the input" flag: the prompt may not
// have been submitted (a slash menu, a backslash Enter). The agent's own
// UserPromptSubmit hook says it really was, and clears it.
func TestUserPromptSubmitClearsTypedFlag(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id := e.agentID(t, "a")
	a, _ := e.d.Agent("w", "a")
	a.Write([]byte("abc\r"))
	if !a.UserTyped() {
		t.Fatal("Enter cleared the flag")
	}
	e.d.onHook(id, hook.Report{Event: "SessionStart"})
	e.d.onHook(id, hook.Report{Event: "Stop"})
	if !a.UserTyped() {
		t.Fatal("other hooks cleared the flag")
	}
	e.d.onHook(id, hook.Report{Event: "UserPromptSubmit"})
	if a.UserTyped() {
		t.Fatal("UserPromptSubmit did not clear the flag")
	}
}

// Prompt A was queued while the agent was busy, the user typed draft B, then A
// was submitted: the hook must not wipe B.
func TestUserPromptSubmitKeepsLaterDraft(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id := e.agentID(t, "a")
	a, _ := e.d.Agent("w", "a")
	a.Write([]byte("prompt A\r"))
	time.Sleep(2 * time.Millisecond)
	a.Write([]byte("draft B"))
	e.d.onHook(id, hook.Report{Event: "UserPromptSubmit"})
	if !a.UserTyped() {
		t.Fatal("UserPromptSubmit wiped a draft typed after the last Enter")
	}
}
