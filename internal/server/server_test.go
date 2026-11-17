package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/cswap"
	"github.com/KarvinNanda/relaydeck/internal/daemon"
	"github.com/KarvinNanda/relaydeck/internal/logx"
	"github.com/KarvinNanda/relaydeck/internal/paths"
)

var fakeBinDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "server-fakeclaude")
	if err != nil {
		panic(err)
	}
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name), "../daemon/testdata/fakeclaude").CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	fakeBinDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

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

const uiToken = "ui-token-for-server-tests"

type rig struct {
	d       *daemon.Daemon
	srv     *httptest.Server
	port    int
	cfgPath string
	out     string
	logs    *syncBuf
	dirA    string
	dirB    string
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

// newRig starts a real HTTP server on 127.0.0.1 with a daemon whose
// workspace "w" has folders "a" and "b". The daemon port equals the
// listener port, so the Host check sees the real port.
func newRig(t *testing.T, static fstest.MapFS) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{
		cfgPath: filepath.Join(base, "cfg", "config.yaml"),
		out:     canon(t, filepath.Join(base, "out")),
		dirA:    canon(t, filepath.Join(base, "a")),
		dirB:    canon(t, filepath.Join(base, "b")),
		logs:    &syncBuf{},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.port = ln.Addr().(*net.TCPAddr).Port
	cfg := &config.Config{Port: config.DefaultPort, Workspaces: []config.Workspace{{Name: "w", Folders: []config.Folder{
		{Name: "a", Path: r.dirA}, {Name: "b", Path: r.dirB},
	}}}}
	if err := cfg.Save(r.cfgPath); err != nil {
		t.Fatal(err)
	}
	log := logx.New(r.logs)
	d, err := daemon.New(daemon.Options{
		ConfigPath:   r.cfgPath,
		Config:       cfg,
		Port:         r.port,
		Log:          log,
		RelaydeckExe: filepath.Join(base, "bin", "relaydeck"),
		RuntimeRoot:  filepath.Join(base, "runtime"),
		BaseEnv:      []string{"FAKECLAUDE_DIR=" + r.out},
		ClaudeHome:   filepath.Join(base, "claudehome"),
		ExternalOpen: func(context.Context, string, string, map[int]bool) (bool, error) { return false, nil },
		OwnPIDs:      func() map[int]bool { return nil },
		CswapStatus:  func(context.Context, string) (cswap.Info, error) { return cswap.Info{}, cswap.ErrUnknownFormat },
		EnterDelay:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	g := &auth.Guard{Port: r.port, UIToken: uiToken, Agents: d.Tokens(), Tickets: auth.NewTicketStore(time.Now)}
	if static == nil {
		static = fstest.MapFS{}
	}
	srv := httptest.NewUnstartedServer(New(d, g, static, log))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	r.srv = srv
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		srv.Close()
	})
	return r
}

func (r *rig) do(t *testing.T, method, path, host, bearer string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Ruling 3: the UI bundle is served without a token, behind the Host and
// Origin checks.
func TestStaticServedWithoutToken(t *testing.T) {
	r := newRig(t, fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>relaydeck</title>")}})
	code, body := r.do(t, "GET", "/", "", "", nil)
	if code != 200 || !strings.Contains(body, "<title>relaydeck</title>") {
		t.Fatalf("GET / = %d %q", code, body)
	}
	if code, _ := r.do(t, "GET", "/", fmt.Sprintf("localhost:%d", r.port), "", nil); code != 200 {
		t.Fatalf("localhost host = %d", code)
	}
	if code, _ := r.do(t, "GET", "/", fmt.Sprintf("evil.example:%d", r.port), "", nil); code != http.StatusMisdirectedRequest {
		t.Fatalf("evil host = %d, want 421", code)
	}
	req, _ := http.NewRequest("GET", r.srv.URL+"/", nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin = %d, want 403", resp.StatusCode)
	}
}

// Agent routes need an agent token: no token and the UI token both get 401,
// and so does an unknown path under /agent/ (nothing is revealed without a token).
func TestAgentRoutesRejectUIToken(t *testing.T) {
	r := newRig(t, nil)
	for _, path := range []string{"/agent/hook", "/agent/tools/list_folders", "/agent/nope"} {
		for _, tok := range []string{"", uiToken, "made-up"} {
			if code, _ := r.do(t, "POST", path, "", tok, map[string]string{"event": "Stop"}); code != http.StatusUnauthorized {
				t.Errorf("POST %s with token %q = %d, want 401", path, tok, code)
			}
		}
	}
	if code, _ := r.do(t, "POST", "/agent/hook", fmt.Sprintf("evil.example:%d", r.port), "", nil); code != http.StatusMisdirectedRequest {
		t.Fatalf("agent route evil host = %d, want 421", code)
	}
}

// Ruling 12 (Review Focus 5): exact message when the port is taken.
func TestListenPortInUse(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	ln, err := Listen(port, "/home/u/.config/relaydeck/config.yaml")
	if err == nil {
		ln.Close()
		t.Fatal("Listen on a busy port succeeded")
	}
	want := fmt.Sprintf(`port %d is already in use. Another relaydeck may be running. Change "port" in /home/u/.config/relaydeck/config.yaml or pass --port.`, port)
	if err.Error() != want {
		t.Fatalf("error = %q\nwant    %q", err.Error(), want)
	}
}

func TestListenLoopbackOnly(t *testing.T) {
	ln, err := Listen(0, "cfg")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ip := ln.Addr().(*net.TCPAddr).IP; !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listening on %v, want 127.0.0.1", ip)
	}
}
