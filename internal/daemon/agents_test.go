package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/notify"
	"github.com/KarvinNanda/relaydeck/internal/paths"
	"github.com/KarvinNanda/relaydeck/internal/screen"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Ruling 8: launch inputs, new session id stored in config, token only in env.
func TestStartAgentNewSessionSavesID(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id := e.agentID(t, "a")
	fi := e.waitInfo(t, id)

	sid := argAfter(fi.Argv, "--session-id")
	if !uuidV4.MatchString(sid) {
		t.Fatalf("--session-id %q is not a lowercase uuid v4 (argv %q)", sid, fi.Argv)
	}
	if got := e.savedFolder(t, "a").SessionID; got != sid {
		t.Fatalf("saved session id = %q, want %q", got, sid)
	}
	if fi.Cwd != e.dirA {
		t.Fatalf("cwd = %q, want %q", fi.Cwd, e.dirA)
	}
	if fi.URL != "http://127.0.0.1:7341" || fi.ID != id || fi.Token == "" {
		t.Fatalf("env url %q id %q (want %q) token set %v", fi.URL, fi.ID, id, fi.Token != "")
	}
	if containsArg(fi.Argv, fi.Token) {
		t.Fatal("agent token appears in argv")
	}
	if got, ok := e.d.reg.Lookup(fi.Token); !ok || got != id {
		t.Fatalf("token from env does not map to agent %q", id)
	}
	rt := filepath.Join(e.opts.RuntimeRoot, id)
	if argAfter(fi.Argv, "--mcp-config") != filepath.Join(rt, "mcp.json") {
		t.Fatalf("--mcp-config = %q, want under %q", argAfter(fi.Argv, "--mcp-config"), rt)
	}
	settings, err := os.ReadFile(filepath.Join(rt, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), e.opts.RelaydeckExe) {
		t.Fatalf("settings.json does not call %q: %s", e.opts.RelaydeckExe, settings)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(rt, "mcp.json"))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mcp.json mode %v err %v", st.Mode().Perm(), err)
		}
	}
}

func TestStartAgentResumeAndPick(t *testing.T) {
	needPTY(t)
	const cur = "11111111-1111-4111-8111-111111111111"
	const picked = "22222222-2222-4222-8222-222222222222"
	e := newEnv(t, func(o *Options) { o.Config.Workspaces[0].Folders[0].SessionID = cur })
	e.transcript(t, e.dirA, cur)
	e.transcript(t, e.dirA, picked)

	cases := []struct {
		sid        string
		newSession bool
		flag, want string
	}{
		{"", false, "--resume", cur},
		{picked, false, "--resume", picked},
		{"", true, "--session-id", ""},
	}
	for _, c := range cases {
		if err := e.d.StartAgent("w", "a", c.sid, c.newSession); err != nil {
			t.Fatal(err)
		}
		fi := e.waitInfo(t, e.agentID(t, "a"))
		got := argAfter(fi.Argv, c.flag)
		if c.want != "" && got != c.want || c.want == "" && !uuidV4.MatchString(got) {
			t.Fatalf("case %+v: %s = %q (argv %q)", c, c.flag, got, fi.Argv)
		}
		if saved := e.savedFolder(t, "a").SessionID; saved != got {
			t.Fatalf("case %+v: saved session id %q, want %q", c, saved, got)
		}
		if err := e.d.StopAgent("w", "a", false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStartAgentErrors(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "nope", "", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown folder: %v", err)
	}
	if err := e.d.StartAgent("x", "a", "", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown workspace: %v", err)
	}
	for _, sid := range []string{"not-a-uuid", "AAAAAAAA-1111-4111-8111-111111111111", "--resume"} {
		if err := e.d.StartAgent("w", "a", sid, false); !errors.Is(err, ErrInvalid) {
			t.Fatalf("session id %q: %v", sid, err)
		}
	}
	if err := e.d.StartAgent("w", "a", "11111111-1111-4111-8111-111111111111", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("session id with new: %v", err)
	}
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	if err := e.d.StartAgent("w", "a", "", false); !errors.Is(err, ErrRunning) {
		t.Fatalf("second start: %v, want ErrRunning", err)
	}
}

// One running agent per canonical path, across workspaces. The config
// allows the same folder in two workspaces (spec 7.2); the second start is
// refused.
func TestStartSamePathOtherWorkspaceConflict(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.AddWorkspace("v"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.AddFolder("v", config.Folder{Name: "a2", Path: e.dirA}); err != nil {
		t.Fatal(err)
	}
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	if err := e.d.StartAgent("v", "a2", "", false); !errors.Is(err, ErrRunning) {
		t.Fatalf("same path in another workspace: %v, want ErrRunning", err)
	}
	if _, ok := e.d.reg.latest(delivery.Target{WS: "v", Folder: "a2"}); ok {
		t.Fatal("failed start was recorded as the folder's agent")
	}
}

// Spec 7.6: never resume a session that is open in an external terminal.
func TestStartRefusesExternalResume(t *testing.T) {
	needPTY(t)
	const cur = "11111111-1111-4111-8111-111111111111"
	var calls int
	e := newEnv(t, func(o *Options) {
		o.Config.Workspaces[0].Folders[0].SessionID = cur
		o.ExternalOpen = func(_ context.Context, dir, sid string, _ map[int]bool) (bool, error) {
			calls++
			return sid == cur, nil
		}
	})
	e.transcript(t, e.dirA, cur)
	if err := e.d.StartAgent("w", "a", "", false); !errors.Is(err, ErrExternalOpen) {
		t.Fatalf("resume while open outside: %v, want ErrExternalOpen", err)
	}
	if calls != 1 {
		t.Fatalf("detector calls = %d", calls)
	}
	// A new session does not touch the external one, so it is not checked.
	if err := e.d.StartAgent("w", "a", "", true); err != nil {
		t.Fatalf("new session: %v", err)
	}
	if calls != 1 {
		t.Fatalf("detector called for a new session")
	}
}

// Ruling 9 and Task 8 carry: a draft blocks a normal stop; force kills.
func TestStopAgentDraft(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id := e.agentID(t, "a")
	fi := e.waitInfo(t, id)
	a, ok := e.d.Agent("w", "a")
	if !ok {
		t.Fatal("Agent not found")
	}
	eventually(t, "empty prompt", func() bool { return a.ScreenState().Ready() })
	if err := a.Write([]byte("half typed")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "draft", a.HasDraft)

	err := e.d.StopAgent("w", "a", false)
	if !errors.Is(err, ErrDraft) || err.Error() != "agent has an unsent draft; clear it or use force" {
		t.Fatalf("stop with draft: %v", err)
	}
	select {
	case <-a.Done():
		t.Fatal("agent stopped despite the draft")
	default:
	}
	start := time.Now()
	if err := e.d.StopAgent("w", "a", true); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("force stop waited %v", time.Since(start))
	}
	if strings.Contains(e.lines(id), "half typed/exit") {
		t.Fatal("force stop submitted the draft with /exit")
	}
	if _, ok := e.d.reg.Lookup(fi.Token); ok {
		t.Fatal("token still valid after stop")
	}
	if err := e.d.StopAgent("w", "a", false); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stop twice: %v", err)
	}
	if err := e.d.StopAgent("w", "zz", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop unknown: %v", err)
	}
}

// Claude Code draws a prompt suggestion in the empty box as plain text. The
// user never typed it, so a normal stop is not refused. (Inject does not
// count as typing, and the terminal echoes it into the box like a suggestion.)
func TestStopAgentSuggestionIsNotDraft(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id := e.agentID(t, "a")
	a, _ := e.d.Agent("w", "a")
	eventually(t, "empty prompt", func() bool { return a.ScreenState().Ready() })
	if err := a.Inject([]byte("cek pesan dari api lagi")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "suggestion on screen", a.HasDraft)
	if a.UserTyped() {
		t.Fatal("UserTyped set by Inject")
	}
	if err := e.d.StopAgent("w", "a", false); err != nil {
		t.Fatalf("stop with a suggestion on screen: %v", err)
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("agent still running after a normal stop")
	}
	if strings.Contains(e.lines(id), "cek pesan") {
		t.Fatalf("the suggestion was submitted: %q", e.lines(id))
	}
}

// The same text typed by the user is a draft and refuses a normal stop.
func TestStopAgentTypedTextIsDraft(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	a, _ := e.d.Agent("w", "a")
	eventually(t, "empty prompt", func() bool { return a.ScreenState().Ready() })
	if err := a.Write([]byte("cek pesan dari api lagi")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "draft", a.HasDraft)
	if err := e.d.StopAgent("w", "a", false); !errors.Is(err, ErrDraft) {
		t.Fatalf("stop with typed text: %v, want ErrDraft", err)
	}
	select {
	case <-a.Done():
		t.Fatal("agent stopped despite the draft")
	default:
	}
}

// Ruling 2: token revoked on any exit; runtime dir removed; a restart drops
// the old agent from the manager.
func TestStopRevokesAndRestartReplaces(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id1 := e.agentID(t, "a")
	fi := e.waitInfo(t, id1)
	// A normal stop types /exit only once the empty box is on screen (it
	// falls back to Kill otherwise), so wait for the fake to draw it.
	a1, ok := e.d.Agent("w", "a")
	if !ok {
		t.Fatal("Agent not found")
	}
	eventually(t, "empty prompt", func() bool { return a1.ScreenState().Ready() })
	if err := e.d.StopAgent("w", "a", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.lines(id1), "/exit") {
		t.Fatalf("normal stop did not send /exit: %q", e.lines(id1))
	}
	if _, ok := e.d.reg.Lookup(fi.Token); ok {
		t.Fatal("token valid after stop")
	}
	eventually(t, "runtime dir removed", func() bool {
		_, err := os.Stat(filepath.Join(e.opts.RuntimeRoot, id1))
		return os.IsNotExist(err)
	})

	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	id2 := e.agentID(t, "a")
	if id2 == id1 {
		t.Fatal("restart reused the agent id")
	}
	if _, ok := e.d.mgr.Get(id1); ok {
		t.Fatal("old agent still in the manager after restart")
	}
	a, ok := e.d.Agent("w", "a")
	if !ok || a.ID() != id2 {
		t.Fatal("Agent does not return the new agent")
	}

	// An agent that exits by itself is revoked too.
	fi2 := e.waitInfo(t, id2)
	a.Inject([]byte("/exit\r"))
	<-a.Done()
	eventually(t, "token revoked after self exit", func() bool {
		_, ok := e.d.reg.Lookup(fi2.Token)
		return !ok
	})
}

// Ruling 7: the user pressing Enter in a panel resets the hop count.
func TestUserEnterResetsHop(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.router.Send("w", "b", "a", "first"); err != nil {
		t.Fatal(err)
	}
	e.d.router.Read("w", "a") // a now handles a hop 1 message
	m, err := e.d.router.Send("w", "a", "b", "reply")
	if err != nil || m.Hop != 2 {
		t.Fatalf("reply hop = %d err %v, want 2", m.Hop, err)
	}
	a, _ := e.d.Agent("w", "a")
	if err := a.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	m, err = e.d.router.Send("w", "a", "b", "user turn")
	if err != nil || m.Hop != 1 {
		t.Fatalf("hop after user Enter = %d err %v, want 1", m.Hop, err)
	}
}

// Ruling 6: ExternalOpen uses the canonical path and current session id,
// excludes relaydeck's own processes, and never checks a running folder.
func TestAgentsAdapterExternalOpen(t *testing.T) {
	needPTY(t)
	const cur = "33333333-3333-4333-8333-333333333333"
	type call struct {
		dir, sid string
		exclude  map[int]bool
	}
	var calls []call
	e := newEnv(t, func(o *Options) {
		o.Config.Workspaces[0].Folders[1].SessionID = cur
		o.OwnPIDs = func() map[int]bool { return map[int]bool{4242: true} }
		o.ExternalOpen = func(_ context.Context, dir, sid string, exclude map[int]bool) (bool, error) {
			calls = append(calls, call{dir, sid, exclude})
			return !exclude[4242], nil
		}
	})
	ad := agentsAdapter{e.d}
	open, err := ad.ExternalOpen("w", "b")
	if err != nil || open {
		t.Fatalf("own pid 4242 must be excluded: open %v err %v", open, err)
	}
	if len(calls) != 1 || calls[0].dir != e.dirB || calls[0].sid != cur || !calls[0].exclude[4242] {
		t.Fatalf("calls = %+v", calls)
	}

	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	calls = nil
	open, err = ad.ExternalOpen("w", "a")
	if err != nil || open || len(calls) != 0 {
		t.Fatalf("running folder: open %v err %v calls %d", open, err, len(calls))
	}
	if !ad.Running("w", "a") || ad.Running("w", "b") {
		t.Fatal("Running wrong")
	}
	if err := ad.Inject("w", "b", []byte("x")); err == nil {
		t.Fatal("Inject into a folder with no agent succeeded")
	}
}

func TestTargetsListsEveryFolder(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.d.AddWorkspace("v"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.AddFolder("v", config.Folder{Name: "c", Path: e.dirC}); err != nil {
		t.Fatal(err)
	}
	got := e.d.targets()
	want := []delivery.Target{{WS: "w", Folder: "a"}, {WS: "w", Folder: "b"}, {WS: "v", Folder: "c"}}
	if len(got) != len(want) {
		t.Fatalf("targets = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("targets = %+v", got)
		}
	}
}

// Ruling 16: shutdown stops every agent, in parallel, and refuses new starts.
func TestShutdownStopsAllInParallel(t *testing.T) {
	needPTY(t)
	e := newEnv(t, func(o *Options) {
		o.BaseEnv = append(o.BaseEnv, "FAKECLAUDE_IGNORE_EXIT=1")
		o.StopGrace = time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.d.Run(ctx); close(done) }()

	for _, f := range []string{"a", "b"} {
		if err := e.d.StartAgent("w", f, "", false); err != nil {
			t.Fatal(err)
		}
		e.waitInfo(t, e.agentID(t, f))
	}
	aa, _ := e.d.Agent("w", "a")
	ab, _ := e.d.Agent("w", "b")
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	// Each fake ignores /exit, so each needs the full 1 s grace. In parallel
	// that is about 1 s, one after the other it would be 2 s.
	if el := time.Since(start); el > 1800*time.Millisecond {
		t.Fatalf("shutdown took %v, agents were not stopped in parallel", el)
	}
	for _, a := range []interface{ Done() <-chan struct{} }{aa, ab} {
		select {
		case <-a.Done():
		default:
			t.Fatal("agent still running after shutdown")
		}
	}
	if err := e.d.StartAgent("w", "a", "", false); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("start after shutdown: %v", err)
	}
}

func TestMessagesApproveRejectDeliverNow(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.d.Messages("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Messages unknown ws: %v", err)
	}
	if err := e.d.DeliverNow("w", "zz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeliverNow unknown folder: %v", err)
	}
	if err := e.d.DeliverNow("w", "a"); err != nil {
		t.Fatal(err)
	}
	// Push "a" to hop 2 so its next message needs approval.
	e.d.router.Send("w", "b", "a", "one")
	e.d.router.Read("w", "a")
	e.d.router.Send("w", "a", "b", "two")
	e.d.router.Read("w", "b")
	held, err := e.d.router.Send("w", "b", "a", "three")
	if err != nil || held.State != notify.StateAwaitingApproval {
		t.Fatalf("held = %+v err %v", held, err)
	}
	msgs, err := e.d.Messages("w")
	if err != nil || len(msgs) != 3 {
		t.Fatalf("Messages = %d err %v", len(msgs), err)
	}
	ta := delivery.Target{WS: "w", Folder: "a"}
	e.d.life.hold(ta) // as after a user stop of "a"
	if err := e.d.Approve(held.ID); err != nil {
		t.Fatal(err)
	}
	if e.d.isHeld(ta) {
		t.Fatal("approving a message for a stopped folder did not lift the hold")
	}
	e.d.life.hold(ta)
	if err := e.d.DeliverNow("w", "a"); err != nil || e.d.isHeld(ta) {
		t.Fatalf("DeliverNow did not lift the hold (err %v)", err)
	}
	if err := e.d.Approve(held.ID); err == nil {
		t.Fatal("approve twice succeeded")
	}
	if err := e.d.Reject("unknown"); err == nil {
		t.Fatal("reject unknown succeeded")
	}
}

func TestSessionsListsFolderSessions(t *testing.T) {
	e := newEnv(t, nil)
	const id = "44444444-4444-4444-8444-444444444444"
	dir := filepath.Join(e.opts.ClaudeHome, "projects", paths.ProjectDirName(e.dirA))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"content":"fixture prompt"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := e.d.Sessions("w", "a")
	if err != nil || len(got) != 1 || got[0].ID != id || got[0].Title != "fixture prompt" {
		t.Fatalf("Sessions = %+v err %v", got, err)
	}
	if _, err := e.d.Sessions("w", "zz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown folder: %v", err)
	}
}

func TestOwnDescendantsIncludesChild(t *testing.T) {
	cmd := testChild(t)
	got := ownDescendants()
	if !got[cmd.Process.Pid] {
		t.Fatalf("child pid %d not in own descendants", cmd.Process.Pid)
	}
	if got[os.Getpid()] {
		t.Fatal("own pid listed as a descendant")
	}
}

// testChild starts a long-running child process (the fake claude waits on stdin).
func testChild(t *testing.T) *exec.Cmd {
	t.Helper()
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cmd := exec.Command(filepath.Join(fakeBinDir, name))
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

// Task 8 carry: at shutdown an agent with a draft is killed without /exit,
// so the draft is never submitted.
func TestShutdownKillsDraftWithoutExit(t *testing.T) {
	needPTY(t)
	e := newEnv(t, func(o *Options) { o.StopGrace = 3 * time.Second })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.d.Run(ctx); close(done) }()
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	a, _ := e.d.Agent("w", "a")
	eventually(t, "empty prompt", func() bool { return a.ScreenState().Ready() })
	a.Write([]byte("draft text"))
	eventually(t, "draft", a.HasDraft)
	start := time.Now()
	cancel()
	<-done
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("shutdown waited %v for an agent with a draft", el)
	}
	if l := e.lines(a.ID()); strings.Contains(l, "/exit") {
		t.Fatalf("shutdown typed /exit after the draft: %q", l)
	}
}

// Fix round 1: /exit is typed only into the plain input box. With an
// overlay, the trust dialog or a permission prompt on screen, Enter would
// pick the highlighted option (trust the folder, approve a tool), so the
// agent is killed instead. A busy footer still gets /exit.
func TestStopTypesExitOnlyIntoPlainBox(t *testing.T) {
	needPTY(t)
	cases := []struct {
		cmd      string
		ready    func(s screen.State) bool
		wantExit bool
	}{
		{"noprompt", func(s screen.State) bool { return !s.InputVisible }, false},
		{"trust", func(s screen.State) bool { return s.Trust }, false},
		{"busy", func(s screen.State) bool { return s.Busy && s.InputVisible }, true},
	}
	for _, shutdown := range []bool{false, true} {
		for _, c := range cases {
			e := newEnv(t, nil)
			if err := e.d.StartAgent("w", "a", "", false); err != nil {
				t.Fatal(err)
			}
			a, _ := e.d.Agent("w", "a")
			eventually(t, "prompt", func() bool { return a.ScreenState().Ready() })
			a.Inject([]byte(c.cmd + "\r"))
			eventually(t, c.cmd+" on screen", func() bool { return c.ready(a.ScreenState()) })
			if shutdown {
				e.d.stopAll()
			} else if err := e.d.StopAgent("w", "a", false); err != nil {
				t.Fatalf("%s: %v", c.cmd, err)
			}
			<-a.Done()
			if got := strings.Contains(e.lines(a.ID()), "/exit"); got != c.wantExit {
				t.Fatalf("shutdown=%v %s: /exit typed = %v, want %v (lines %q)", shutdown, c.cmd, got, c.wantExit, e.lines(a.ID()))
			}
		}
	}
}

// Fix round 1 (3a): --resume needs the session file. Without it claude exits
// at once. A picked session with no file is a clear error; the folder's own
// current session with no file (for example started, then stopped before any
// prompt) falls back to a new session, for the user and for delivery alike.
func TestResumeNeedsTranscript(t *testing.T) {
	needPTY(t)
	const cur = "88888888-8888-4888-8888-888888888888"
	const picked = "99999999-9999-4999-8999-999999999999"
	e := newEnv(t, func(o *Options) { o.Config.Workspaces[0].Folders[0].SessionID = cur })

	if err := e.d.StartAgent("w", "a", picked, false); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("picked session without a file: %v, want ErrNoTranscript", err)
	}
	for _, viaDelivery := range []bool{false, true} {
		var err error
		if viaDelivery {
			err = agentsAdapter{e.d}.Start("w", "a")
		} else {
			err = e.d.StartAgent("w", "a", "", false)
		}
		if err != nil {
			t.Fatalf("delivery=%v: %v", viaDelivery, err)
		}
		fi := e.waitInfo(t, e.agentID(t, "a"))
		sid := argAfter(fi.Argv, "--session-id")
		if argAfter(fi.Argv, "--resume") != "" || !uuidV4.MatchString(sid) || sid == cur {
			t.Fatalf("delivery=%v: argv %q, want a new session", viaDelivery, fi.Argv)
		}
		if got := e.savedFolder(t, "a").SessionID; got != sid {
			t.Fatalf("delivery=%v: saved %q, want the new id %q", viaDelivery, got, sid)
		}
		if err := e.d.StopAgent("w", "a", false); err != nil {
			t.Fatal(err)
		}
		// Next round starts from a current session that again has no file.
		e.d.setSessionID("w", "a", cur)
	}
}

// Fix round 1 (3b): an agent that exits right after it starts is not
// restarted by delivery in a tight loop.
func TestQuickExitIsNotRestartedInALoop(t *testing.T) {
	needPTY(t)
	e := newEnv(t, func(o *Options) { o.BaseEnv = append(o.BaseEnv, "FAKECLAUDE_EXIT_AT_ONCE=1") })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.d.Run(ctx)
	if _, err := e.d.router.Send("w", "a", "b", "hello"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "first start", func() bool { return e.startsOf(t) >= 1 })
	time.Sleep(3 * time.Second)
	if n := e.startsOf(t); n > 2 {
		t.Fatalf("%d starts within 3 s of a crash on start", n)
	}
	if p := e.d.router.Pending("w", "b"); len(p) != 1 {
		t.Fatalf("pending = %d, the message must stay queued", len(p))
	}
	// The user can still start it by hand.
	if err := e.d.StartAgent("w", "b", "", false); err != nil {
		t.Fatalf("user start after quick exit: %v", err)
	}
}

// Fix round 1 (controller): after the user stops an agent, delivery does not
// start it again for messages that were already waiting. A new message, or
// the user starting it, lifts that.
func TestUserStopHoldsAutoStart(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.d.Run(ctx)
	if err := e.d.StartAgent("w", "b", "", false); err != nil {
		t.Fatal(err)
	}
	// No SessionStart hook: b stays "starting", so the message waits queued.
	if _, err := e.d.router.Send("w", "a", "b", "old"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.StopAgent("w", "b", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond) // two ticks and more
	if a := e.d.running(delivery.Target{WS: "w", Folder: "b"}); a != nil {
		t.Fatal("delivery restarted an agent the user stopped")
	}
	if p := e.d.router.Pending("w", "b"); len(p) != 1 || p[0].State != notify.StateQueued {
		t.Fatalf("pending = %+v, want the old message still queued", p)
	}
	if _, err := e.d.router.Send("w", "a", "b", "new"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "restart for a new message", func() bool {
		return e.d.running(delivery.Target{WS: "w", Folder: "b"}) != nil
	})

	// A user start also lifts the hold.
	tb := delivery.Target{WS: "w", Folder: "b"}
	if err := e.d.StopAgent("w", "b", false); err != nil {
		t.Fatal(err)
	}
	if !e.d.isHeld(tb) {
		t.Fatal("user stop did not hold auto start")
	}
	if err := e.d.StartAgent("w", "b", "", false); err != nil {
		t.Fatal(err)
	}
	if e.d.isHeld(tb) {
		t.Fatal("user start did not lift the hold")
	}
}

// A missing claude command is ErrClaudeNotFound (not the raw exec error),
// and the failed start leaves nothing registered.
func TestStartClaudeNotOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH lookup differs on windows")
	}
	t.Setenv("PATH", t.TempDir())
	e := newEnv(t, nil)
	err := e.d.StartAgent("w", "a", "", false)
	if !errors.Is(err, ErrClaudeNotFound) || err.Error() != "claude is not installed or not on PATH" {
		t.Fatalf("start without claude: %v", err)
	}
	if _, ok := e.d.Agent("w", "a"); ok {
		t.Fatal("agent registered after a failed start")
	}
}

// When the external session check itself fails, the start is refused with
// ErrExternalCheck, and the text keeps the cause.
func TestStartExternalCheckFails(t *testing.T) {
	const cur = "11111111-1111-4111-8111-111111111111"
	e := newEnv(t, func(o *Options) {
		o.Config.Workspaces[0].Folders[0].SessionID = cur
		o.ExternalOpen = func(context.Context, string, string, map[int]bool) (bool, error) {
			return false, errors.New("ps failed")
		}
	})
	e.transcript(t, e.dirA, cur)
	err := e.d.StartAgent("w", "a", "", false)
	want := "could not check for an open Claude Code session in this folder: ps failed"
	if !errors.Is(err, ErrExternalCheck) || err.Error() != want {
		t.Fatalf("start = %v, want %q", err, want)
	}
}
