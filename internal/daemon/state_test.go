package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/cswap"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/hook"
)

func TestAddWorkspaceAndFolder(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.d.AddWorkspace("w"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate workspace: %v", err)
	}
	if err := e.d.AddWorkspace("Bad Name"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad workspace name: %v", err)
	}
	if err := e.d.AddWorkspace("v"); err != nil {
		t.Fatal(err)
	}

	// The stored path is canonical: a symlink is resolved.
	real := canon(t, filepath.Join(t.TempDir(), "real dir"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks not available:", err)
	}
	f, err := e.d.AddFolder("v", config.Folder{Name: "c", Path: link, Account: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != real {
		t.Fatalf("stored path %q, want %q", f.Path, real)
	}
	c, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	_, saved, ok := c.Folder("v", "c")
	if !ok || saved.Path != real || saved.Account != "2" {
		t.Fatalf("saved folder = %+v ok %v", saved, ok)
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(file, []byte("x"), 0o600)
	cases := []struct {
		ws   string
		f    config.Folder
		want error
	}{
		{"v", config.Folder{Name: "d", Path: file}, ErrInvalid},
		{"v", config.Folder{Name: "d", Path: filepath.Join(real, "missing")}, ErrInvalid},
		{"v", config.Folder{Name: "c", Path: real}, ErrConflict},
		{"v", config.Folder{Name: "UPPER", Path: real}, ErrInvalid},
		{"v", config.Folder{Name: "d", Path: real, PermissionMode: "yolo"}, ErrInvalid},
		{"nope", config.Folder{Name: "d", Path: real}, ErrNotFound},
	}
	for _, tc := range cases {
		if _, err := e.d.AddFolder(tc.ws, tc.f); !errors.Is(err, tc.want) {
			t.Fatalf("AddFolder(%q, %+v) = %v, want %v", tc.ws, tc.f, err, tc.want)
		}
	}
	// A failed add leaves memory and disk unchanged.
	if n := len(e.d.State().Workspaces[1].Folders); n != 1 {
		t.Fatalf("workspace v has %d folders after failed adds", n)
	}
}

// Ruling 17: the account is dropped on windows.
func TestAddFolderDropsAccountOnWindows(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.GOOS = "windows" })
	f, err := e.d.AddFolder("w", config.Folder{Name: "c", Path: e.dirB, Account: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Account != "" || e.savedFolder(t, "c").Account != "" {
		t.Fatal("account kept on windows")
	}
}

// Ruling 1: every config read, mutation and Save happens under one mutex.
// Run with -race.
func TestConfigConcurrent(t *testing.T) {
	e := newEnv(t, nil)
	idA, _, _ := e.d.reg.issue(delivery.Target{WS: "w", Folder: "a"})
	var wg sync.WaitGroup
	const n = 12
	for i := 0; i < n; i++ {
		wg.Add(4)
		go func(i int) {
			defer wg.Done()
			dir := canon(t, filepath.Join(t.TempDir(), fmt.Sprint(i)))
			if _, err := e.d.AddFolder("w", config.Folder{Name: fmt.Sprintf("f%d", i), Path: dir}); err != nil {
				t.Error(err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			e.d.onHook(idA, hook.Report{Event: "SessionStart", SessionID: fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)})
		}(i)
		go func() { defer wg.Done(); _ = e.d.State() }()
		go func() { defer wg.Done(); _ = e.d.targets(); _ = e.d.dir.Folders("w") }()
	}
	wg.Wait()
	c, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.Workspaces[0].Folders); got != n+2 {
		t.Fatalf("saved %d folders, want %d", got, n+2)
	}
	if got := len(e.d.State().Workspaces[0].Folders); got != n+2 {
		t.Fatalf("memory has %d folders, want %d", got, n+2)
	}
}

// Ruling 4: the router and the MCP tools share one Directory, so a folder
// added at runtime is visible to both at once.
func TestDirectorySeesNewFolders(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.d.router.Send("w", "a", "c", "hi"); err == nil {
		t.Fatal("send to a folder that does not exist yet succeeded")
	}
	if _, err := e.d.AddFolder("w", config.Folder{Name: "c", Path: e.dirB}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.router.Send("w", "a", "c", "hi"); err != nil {
		t.Fatalf("send after add: %v", err)
	}
	if got := e.d.dir.Folders("w"); len(got) != 3 {
		t.Fatalf("Folders = %v", got)
	}
	if got := e.d.dir.Folders("nope"); len(got) != 0 {
		t.Fatalf("Folders(unknown) = %v", got)
	}
}

func TestStateSnapshot(t *testing.T) {
	needPTY(t)
	e := newEnv(t, func(o *Options) {
		o.Config.Workspaces[0].Folders[1].Account = "2"
		o.Config.Workspaces[0].Folders[1].PermissionMode = "plan"
	})
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	a, _ := e.d.Agent("w", "a")
	eventually(t, "prompt drawn", func() bool { return a.ScreenState().InputVisible })
	e.d.onHook(a.ID(), hook.Report{Event: "SessionStart"})
	e.d.router.Send("w", "a", "b", "one")
	e.d.router.Send("w", "a", "b", "two")

	st := e.d.State()
	if st.Platform != runtime.GOOS || len(st.Workspaces) != 1 || st.Workspaces[0].Name != "w" {
		t.Fatalf("state = %+v", st)
	}
	fa, fb := st.Workspaces[0].Folders[0], st.Workspaces[0].Folders[1]
	if fa.Name != "a" || fa.Path != e.dirA || fa.Status != agent.StatusIdle || fa.Screen != "" || fa.PermissionMode != "auto" {
		t.Fatalf("folder a = %+v", fa)
	}
	if fb.Status != agent.StatusStopped || fb.Pending != 2 || fb.PermissionMode != "plan" || fb.Account != "2" {
		t.Fatalf("folder b = %+v", fb)
	}
	if fb.OnDefaultLogin || st.Account.Known {
		t.Fatal("account must be unknown without cswap")
	}

	// A screen with no input box reads as waiting_for_prompt.
	a.Inject([]byte("noprompt\r"))
	eventually(t, "box gone", func() bool { return !a.ScreenState().InputVisible })
	if got := e.d.State().Workspaces[0].Folders[0].Screen; got != "waiting_for_prompt" {
		t.Fatalf("screen = %q", got)
	}
}

// Ruling 13: cswap status polled; default account exposed; status errors
// mean "account unknown"; a folder on the default account is flagged.
func TestCswapPoll(t *testing.T) {
	var mu sync.Mutex
	answer := cswap.Info{DefaultAccount: "2"}
	var answerErr error
	e := newEnv(t, func(o *Options) {
		o.CswapPath = "/opt/cswap"
		o.CswapEvery = 20 * time.Millisecond
		o.Config.Workspaces[0].Folders[0].Account = "2"
		o.Config.Workspaces[0].Folders[1].Account = "3"
		o.CswapStatus = func(_ context.Context, path string) (cswap.Info, error) {
			mu.Lock()
			defer mu.Unlock()
			if path != "/opt/cswap" {
				return cswap.Info{}, errors.New("wrong path")
			}
			return answer, answerErr
		}
	})
	ev, cancelEv := e.d.Subscribe()
	defer cancelEv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.d.Run(ctx)

	eventually(t, "account known", func() bool { return e.d.State().Account.Known })
	st := e.d.State()
	if !st.Account.Cswap || st.Account.Default != "2" {
		t.Fatalf("account = %+v", st.Account)
	}
	if !st.Workspaces[0].Folders[0].OnDefaultLogin || st.Workspaces[0].Folders[1].OnDefaultLogin {
		t.Fatalf("on default login flags wrong: %+v", st.Workspaces[0].Folders)
	}
	sawAccount := false
	for !sawAccount {
		select {
		case x := <-ev:
			sawAccount = x.Type == "account" && x.Account != nil && x.Account.Default == "2"
		case <-time.After(2 * time.Second):
			t.Fatal("no account event")
		}
	}

	mu.Lock()
	answerErr = errors.New("cswap broke")
	mu.Unlock()
	eventually(t, "account unknown", func() bool { return !e.d.State().Account.Known })
	st = e.d.State()
	if st.Account.Default != "" || st.Workspaces[0].Folders[0].OnDefaultLogin {
		t.Fatalf("guessed an account after an error: %+v", st.Account)
	}
}

func TestNoCswapNoPoll(t *testing.T) {
	called := make(chan struct{}, 1)
	e := newEnv(t, func(o *Options) {
		o.CswapStatus = func(context.Context, string) (cswap.Info, error) {
			called <- struct{}{}
			return cswap.Info{}, nil
		}
		o.CswapEvery = 10 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(context.Background())
	go e.d.Run(ctx)
	select {
	case <-called:
		t.Fatal("cswap polled without a cswap path")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if e.d.State().Account.Cswap {
		t.Fatal("cswap reported present")
	}
}

// 13b streams these: status events carry the folder, message events the message.
func TestSubscribeEvents(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	ev, cancelEv := e.d.Subscribe()
	defer cancelEv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.d.Run(ctx)

	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	e.d.router.Send("w", "a", "b", "x")
	var sawStatus, sawMsg bool
	for !sawStatus || !sawMsg {
		select {
		case x := <-ev:
			switch x.Type {
			case "status":
				sawStatus = sawStatus || (x.WS == "w" && x.Folder == "a" && x.Status == agent.StatusStarting)
			case "message":
				sawMsg = sawMsg || (x.Message != nil && x.Message.To == "b" && x.WS == "w")
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("events: status %v message %v", sawStatus, sawMsg)
		}
	}
}

func TestNewRequiresOptions(t *testing.T) {
	e := newEnv(t, nil)
	for name, mut := range map[string]func(*Options){
		"config path": func(o *Options) { o.ConfigPath = "" },
		"config":      func(o *Options) { o.Config = nil },
		"port":        func(o *Options) { o.Port = 0 },
		"exe":         func(o *Options) { o.RelaydeckExe = "" },
		"runtime":     func(o *Options) { o.RuntimeRoot = "relative" },
		"claude home": func(o *Options) { o.ClaudeHome = "" },
		"log":         func(o *Options) { o.Log = nil },
	} {
		o := e.opts
		mut(&o)
		if _, err := New(o); err == nil {
			t.Errorf("New without %s succeeded", name)
		}
	}
}
