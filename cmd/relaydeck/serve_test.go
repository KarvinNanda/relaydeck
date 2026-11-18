package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/config"
)

// isolate points the config dir, cache dir and PATH at temp dirs so serve
// never touches the real config or runs a real cswap or claude.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(home, "AppData", "Local"))
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	p, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Ruling 12 (Review Focus 5): a busy port exits 1 with the exact message.
func TestPortInUseMessage(t *testing.T) {
	cfgPath := isolate(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port

	var out, errb strings.Builder
	code := serve(context.Background(), []string{"--port", fmt.Sprint(port)}, &out, &errb)
	want := fmt.Sprintf("port %d is already in use. Another relaydeck may be running. Change \"port\" in %s or pass --port.\n", port, cfgPath)
	if code != 1 || errb.String() != want || out.Len() != 0 {
		t.Fatalf("code %d\nstderr %q\nwant   %q\nstdout %q", code, errb.String(), want, out.String())
	}
}

// Rulings 15 and 16: one startup line with the UI token on stdout; the
// server answers; cancelling (what SIGINT/SIGTERM do) stops it with exit 0.
func TestServeStartsAndStops(t *testing.T) {
	isolate(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	pr, pw := io.Pipe()
	var errb strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		code := serve(ctx, []string{"--port", fmt.Sprint(port)}, pw, &errb)
		pw.Close()
		done <- code
	}()

	sc := bufio.NewScanner(pr)
	if !sc.Scan() {
		t.Fatalf("no startup line; stderr %q", errb.String())
	}
	line := sc.Text()
	re := regexp.MustCompile(fmt.Sprintf(`^relaydeck running: http://127\.0\.0\.1:%d/#token=([A-Za-z0-9_-]{43})$`, port))
	m := re.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("startup line %q", line)
	}
	token := m[1]

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", base+"/agent/hook", strings.NewReader(`{"event":"Stop"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("UI token on the agent route = %d, want 401", resp.StatusCode)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d, stderr %q", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
	if sc.Scan() {
		t.Fatalf("more than one stdout line: %q", sc.Text())
	}
	if strings.Contains(errb.String(), token) {
		t.Fatal("UI token written to the log")
	}
}

// Fix round 1: bare "relaydeck" (and "relaydeck --port N") runs serve. A
// busy port proves it reached serve without starting a long-running daemon.
func TestBareRunIsServe(t *testing.T) {
	cfgPath := isolate(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	cfg := &config.Config{Port: port}
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("port %d is already in use.", port)
	for _, args := range [][]string{nil, {"--port", fmt.Sprint(port)}} {
		var out, errb strings.Builder
		if code := run(args, &out, &errb); code != 1 || !strings.HasPrefix(errb.String(), want) {
			t.Fatalf("run(%q) = %d, stderr %q", args, code, errb.String())
		}
	}
}

func TestWarnIfNoUI(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	warnIfNoUI(log, fstest.MapFS{"assets/app.js": {Data: []byte("x")}})
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, `web UI not built: run \"cd web && npm ci && npm run build\" before go build`) {
		t.Fatalf("log = %q", out)
	}
	buf.Reset()
	warnIfNoUI(log, fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}})
	if buf.Len() != 0 {
		t.Fatalf("warned although index.html exists: %q", buf.String())
	}
}
