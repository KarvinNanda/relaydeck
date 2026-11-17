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

// One running agent per canonical path, across workspaces.
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
	if _, err := e.d.AddFolder("v", config.Folder{Name: "c", Path: e.dirB}); err != nil {
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
	if err := e.d.Approve(held.ID); err != nil {
		t.Fatal(err)
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
