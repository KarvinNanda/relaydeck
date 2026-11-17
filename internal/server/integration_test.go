package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/daemon"
	"github.com/KarvinNanda/relaydeck/internal/notify"
)

type fakeInfo struct {
	Argv  []string `json:"argv"`
	URL   string   `json:"url"`
	ID    string   `json:"id"`
	Token string   `json:"token"`
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (r *rig) info(t *testing.T, id string) fakeInfo {
	t.Helper()
	var fi fakeInfo
	waitFor(t, "fake info "+id, 5*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(r.out, id+".json"))
		return err == nil && json.Unmarshal(b, &fi) == nil
	})
	return fi
}

func (r *rig) lines(id string) string {
	b, _ := os.ReadFile(filepath.Join(r.out, id+".lines"))
	return string(b)
}

func (r *rig) folder(name string) daemon.FolderState {
	for _, f := range r.d.State().Workspaces[0].Folders {
		if f.Name == name {
			return f
		}
	}
	return daemon.FolderState{}
}

func (r *rig) message(t *testing.T, id string) notify.Message {
	t.Helper()
	msgs, err := r.d.Messages("w")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("message %s not in timeline", id)
	return notify.Message{}
}

func (r *rig) tool(t *testing.T, token, name string, body any) (int, string) {
	t.Helper()
	if body == nil {
		body = struct{}{}
	}
	return r.do(t, "POST", "/agent/tools/"+name, "", token, body)
}

// End to end through the real launch and agent path, with the fake claude on
// PATH, two folders in one workspace, and the agent endpoints driven over a
// real HTTP server with the agents' own tokens (read from the env the fake
// received, never from argv).
func TestIntegrationHooksNotifyDelivery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY is not reliable on CI runners; PTY tests run on unix only")
	}
	t.Setenv("PATH", fakeBinDir)
	r := newRig(t, nil)

	// Start A.
	if err := r.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	aa, _ := r.d.Agent("w", "a")
	infoA := r.info(t, aa.ID())
	if infoA.URL != r.srv.URL || infoA.ID != aa.ID() {
		t.Fatalf("agent env url %q id %q, want %q %q", infoA.URL, infoA.ID, r.srv.URL, aa.ID())
	}

	// Hooks change the status; SessionStart stores the session id.
	const sidA = "aaaaaaaa-0000-4000-8000-000000000001"
	hook := func(token, event, sid string) int {
		code, _ := r.do(t, "POST", "/agent/hook", "", token, map[string]string{"event": event, "session_id": sid})
		return code
	}
	if c := hook(infoA.Token, "SessionStart", sidA); c != 204 {
		t.Fatalf("hook SessionStart = %d", c)
	}
	if st := r.folder("a").Status; st != agent.StatusIdle {
		t.Fatalf("after SessionStart status = %s", st)
	}
	saved, _ := config.Load(r.cfgPath)
	if _, f, _ := saved.Folder("w", "a"); f.SessionID != sidA {
		t.Fatalf("saved session id = %q", f.SessionID)
	}
	hook(infoA.Token, "UserPromptSubmit", "")
	if st := r.folder("a").Status; st != agent.StatusBusy {
		t.Fatalf("after UserPromptSubmit status = %s", st)
	}
	hook(infoA.Token, "Stop", "")
	if st := r.folder("a").Status; st != agent.StatusIdle {
		t.Fatalf("after Stop status = %s", st)
	}

	code, body := r.tool(t, infoA.Token, "list_folders", nil)
	if code != 200 || !strings.Contains(body, "a (idle) (you)") || !strings.Contains(body, "b (stopped)") {
		t.Fatalf("list_folders = %d %s", code, body)
	}

	// A notifies B, which is not running: delivery starts it.
	const secretBody = "start your dev server on port 5173"
	code, body = r.tool(t, infoA.Token, "notify", map[string]string{"target": "b", "message": secretBody})
	if code != 200 || !strings.Contains(body, `queued for \"b\"`) {
		t.Fatalf("notify = %d %s", code, body)
	}
	msgs, _ := r.d.Messages("w")
	msgID := msgs[0].ID

	var ab *agent.Agent
	waitFor(t, "B started by delivery", 5*time.Second, func() bool {
		a, ok := r.d.Agent("w", "b")
		ab = a
		return ok
	})
	infoB := r.info(t, ab.ID())
	if infoB.Token == infoA.Token {
		t.Fatal("B got A's token")
	}
	if !strings.Contains(strings.Join(infoB.Argv, " "), "--session-id") {
		t.Fatalf("B argv = %q, want a new session", infoB.Argv)
	}

	// B reports idle; the screen shows an empty prompt; the nudge is typed.
	waitFor(t, "B prompt drawn", 5*time.Second, func() bool { return ab.ScreenState().Ready() })
	if c := hook(infoB.Token, "SessionStart", ""); c != 204 {
		t.Fatalf("B SessionStart = %d", c)
	}
	const nudge = "[relaydeck] 1 new message. Call read_messages."
	waitFor(t, "nudge typed into B", 5*time.Second, func() bool { return strings.Contains(r.lines(ab.ID()), nudge) })
	if m := r.message(t, msgID); m.State != notify.StateNudged {
		t.Fatalf("message state = %s, want nudged", m.State)
	}
	if strings.Contains(r.lines(ab.ID()), secretBody) {
		t.Fatal("message body was typed into the PTY")
	}

	// The nudge's own prompt confirms it: no requeue after the 3 s window,
	// no second nudge.
	if c := hook(infoB.Token, "UserPromptSubmit", ""); c != 204 {
		t.Fatalf("B UserPromptSubmit = %d", c)
	}
	if st := r.folder("b").Status; st != agent.StatusBusy {
		t.Fatalf("B status = %s, want busy", st)
	}
	hook(infoB.Token, "Stop", "")
	// Confirm window (3 s) plus one tick (1 s) plus margin.
	time.Sleep(4500 * time.Millisecond)
	if m := r.message(t, msgID); m.State != notify.StateNudged {
		t.Fatalf("after the confirm window state = %s, want nudged", m.State)
	}
	if n := strings.Count(r.lines(ab.ID()), nudge); n != 1 {
		t.Fatalf("nudged %d times", n)
	}

	// B pulls the body through read_messages; A sees none of it.
	code, body = r.tool(t, infoB.Token, "read_messages", nil)
	if code != 200 || !strings.Contains(body, secretBody) || !strings.Contains(body, "not by the user") {
		t.Fatalf("read_messages = %d %s", code, body)
	}
	if m := r.message(t, msgID); m.State != notify.StateDelivered {
		t.Fatalf("state after read = %s", m.State)
	}
	if _, body = r.tool(t, infoA.Token, "read_messages", nil); !strings.Contains(body, "No messages.") {
		t.Fatalf("A read B's messages: %s", body)
	}

	// Stop A: its token stops working at once.
	if err := r.d.StopAgent("w", "a", false); err != nil {
		t.Fatal(err)
	}
	if c := hook(infoA.Token, "Stop", ""); c != 401 {
		t.Fatalf("hook with a stopped agent's token = %d, want 401", c)
	}

	// Ruling 14: JSON lines, request_id on requests, agent identity on agent
	// calls, and never a token or a message body.
	logs := r.logs.String()
	for _, secret := range []string{infoA.Token, infoB.Token, secretBody, uiToken} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain a secret or body: %q", secret)
		}
	}
	var sawRequest, sawAgentCall bool
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if m["msg"] == "request" && m["request_id"] != "" {
			sawRequest = true
		}
		if m["msg"] == "agent call" && m["agent_id"] == ab.ID() && m["ws"] == "w" && m["folder"] == "b" && m["request_id"] != "" {
			sawAgentCall = true
		}
	}
	if !sawRequest || !sawAgentCall {
		t.Fatalf("request line %v, agent call line %v", sawRequest, sawAgentCall)
	}
}
