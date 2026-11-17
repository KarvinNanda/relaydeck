package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/cswap"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/logx"
	"github.com/KarvinNanda/relaydeck/internal/paths"
)

// fakeBinDir holds the fake claude, built once, named "claude" so that
// launch.Build's argv[0] resolves to it through PATH.
var fakeBinDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "daemon-fakeclaude")
	if err != nil {
		panic(err)
	}
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name), "./testdata/fakeclaude").CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	fakeBinDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testEnv struct {
	d       *Daemon
	opts    Options
	cfgPath string
	out     string // FAKECLAUDE_DIR
	dirA    string
	dirB    string
	logs    *syncBuf
}

// needPTY skips on windows (ConPTY is unreliable on CI runners, same as the
// agent package) and points PATH at the fake claude only.
func needPTY(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY is not reliable on CI runners; PTY tests run on unix only")
	}
	t.Setenv("PATH", fakeBinDir)
}

func canon(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := paths.Canonicalize(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newEnv builds a daemon with workspace "w" holding folders "a" and "b".
func newEnv(t *testing.T, mutate func(*Options)) *testEnv {
	t.Helper()
	base := t.TempDir()
	e := &testEnv{
		cfgPath: filepath.Join(base, "cfg", "config.yaml"),
		out:     canon(t, filepath.Join(base, "out")),
		dirA:    canon(t, filepath.Join(base, "proj a")),
		dirB:    canon(t, filepath.Join(base, "proj-b")),
		logs:    &syncBuf{},
	}
	cfg := &config.Config{Port: 7341, Workspaces: []config.Workspace{{Name: "w", Folders: []config.Folder{
		{Name: "a", Path: e.dirA},
		{Name: "b", Path: e.dirB},
	}}}}
	e.opts = Options{
		ConfigPath:   e.cfgPath,
		Config:       cfg,
		Port:         7341,
		Log:          logx.New(e.logs),
		RelaydeckExe: filepath.Join(base, "bin", "relaydeck"),
		RuntimeRoot:  filepath.Join(base, "runtime"),
		BaseEnv:      []string{"FAKECLAUDE_DIR=" + e.out},
		GOOS:         runtime.GOOS,
		ClaudeHome:   filepath.Join(base, "claudehome"),
		ExternalOpen: func(context.Context, string, string, map[int]bool) (bool, error) {
			return false, nil
		},
		OwnPIDs:     func() map[int]bool { return map[int]bool{} },
		CswapStatus: func(context.Context, string) (cswap.Info, error) { return cswap.Info{}, cswap.ErrUnknownFormat },
		EnterDelay:  10 * time.Millisecond,
		StopGrace:   3 * time.Second,
	}
	if mutate != nil {
		mutate(&e.opts)
	}
	if err := cfg.Save(e.cfgPath); err != nil {
		t.Fatal(err)
	}
	d, err := New(e.opts)
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	t.Cleanup(func() { d.stopAll() })
	return e
}

type fakeInfo struct {
	Argv  []string `json:"argv"`
	Cwd   string   `json:"cwd"`
	URL   string   `json:"url"`
	ID    string   `json:"id"`
	Token string   `json:"token"`
}

// waitInfo waits until the fake claude for agentID wrote its info file.
func (e *testEnv) waitInfo(t *testing.T, agentID string) fakeInfo {
	t.Helper()
	p := filepath.Join(e.out, agentID+".json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(p)
		if err == nil {
			var fi fakeInfo
			if err := json.Unmarshal(b, &fi); err != nil {
				t.Fatal(err)
			}
			return fi
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake claude %s never wrote its info", agentID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *testEnv) lines(agentID string) string {
	b, _ := os.ReadFile(filepath.Join(e.out, agentID+".lines"))
	return string(b)
}

func (e *testEnv) agentID(t *testing.T, folder string) string {
	t.Helper()
	id, ok := e.d.reg.latest(delivery.Target{WS: "w", Folder: folder})
	if !ok {
		t.Fatalf("no agent for %s", folder)
	}
	return id
}

func (e *testEnv) savedFolder(t *testing.T, name string) config.Folder {
	t.Helper()
	c, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	_, f, ok := c.Folder("w", name)
	if !ok {
		t.Fatalf("folder %s not in saved config", name)
	}
	return *f
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func argAfter(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

func containsArg(argv []string, s string) bool {
	for _, a := range argv {
		if strings.Contains(a, s) {
			return true
		}
	}
	return false
}
