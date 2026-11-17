package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/paths"
)

type fakePicker struct {
	mu    sync.Mutex
	calls int
	path  string
	err   error
}

func (p *fakePicker) PickFolder(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.path, p.err
}

func (p *fakePicker) set(path string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.path, p.err = path, err
}

func (p *fakePicker) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// api calls a UI route with the UI token.
func (r *rig) api(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	return r.do(t, method, path, "", uiToken, body)
}

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

func errText(t *testing.T, body string) string {
	t.Helper()
	return decode[map[string]string](t, body)["error"]
}

// noAgents points PATH at an empty dir, so a start can never launch a real
// claude from the developer's PATH.
func noAgents(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

var apiRoutes = []struct{ method, path string }{
	{"GET", "/api/state"},
	{"POST", "/api/workspaces"},
	{"POST", "/api/workspaces/w/folders"},
	{"POST", "/api/pick-folder"},
	{"GET", "/api/workspaces/w/folders/a/sessions"},
	{"POST", "/api/agents/w/a/start"},
	{"POST", "/api/agents/w/a/stop"},
	{"GET", "/api/workspaces/w/messages"},
	{"POST", "/api/messages/m1/approve"},
	{"POST", "/api/messages/m1/reject"},
	{"POST", "/api/agents/w/a/deliver-now"},
	{"POST", "/api/ws-ticket"},
}

// Every /api route needs the UI token (401 without it or with any other
// token), and the Host and Origin checks run first (421, 403). A refused
// request has no side effect.
func TestRoutesRequireUIToken(t *testing.T) {
	noAgents(t)
	r := newRig(t, nil)
	evilHost := fmt.Sprintf("evil.example:%d", r.port)
	body := map[string]string{"name": "x", "path": r.dirA}
	for _, rt := range append(apiRoutes, struct{ method, path string }{"GET", "/api/nope"}) {
		for _, tok := range []string{"", "made-up", uiToken + "x"} {
			if code, _ := r.do(t, rt.method, rt.path, "", tok, body); code != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q = %d, want 401", rt.method, rt.path, tok, code)
			}
		}
		if code, _ := r.do(t, rt.method, rt.path, evilHost, uiToken, body); code != http.StatusMisdirectedRequest {
			t.Errorf("%s %s evil host = %d, want 421", rt.method, rt.path, code)
		}
		req, _ := http.NewRequest(rt.method, r.srv.URL+rt.path, nil)
		req.Header.Set("Authorization", "Bearer "+uiToken)
		req.Header.Set("Origin", "http://evil.example")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s evil origin = %d, want 403", rt.method, rt.path, resp.StatusCode)
		}
	}
	if r.picker.count() != 0 {
		t.Fatal("folder dialog opened by a refused request")
	}
	if n := len(r.d.State().Workspaces); n != 1 {
		t.Fatalf("refused request added a workspace: %d", n)
	}

	// With the token every listed route exists (no 401, 404 or 405).
	r.picker.set("", ErrPickCancelled)
	for _, rt := range apiRoutes {
		code, _ := r.api(t, rt.method, rt.path, nil)
		if code == 401 || code == 405 || (code == 404 && !strings.HasPrefix(rt.path, "/api/messages/")) {
			t.Errorf("%s %s with token = %d", rt.method, rt.path, code)
		}
	}
	if code, _ := r.api(t, "GET", "/api/nope", nil); code != 404 {
		t.Errorf("unknown api route = %d, want 404", code)
	}
}

func TestStateJSON(t *testing.T) {
	r := newRig(t, nil)
	code, body := r.api(t, "GET", "/api/state", nil)
	if code != 200 {
		t.Fatalf("state = %d %s", code, body)
	}
	for _, k := range []string{`"platform":`, `"account":`, `"workspaces":`, `"permission_mode":"auto"`, `"on_default_login":false`, `"status":"stopped"`} {
		if !strings.Contains(body, k) {
			t.Errorf("state lacks %s: %s", k, body)
		}
	}
}

func TestAddWorkspaceRoute(t *testing.T) {
	r := newRig(t, nil)
	if code, body := r.api(t, "POST", "/api/workspaces", map[string]string{"name": "v"}); code != 201 || decode[map[string]string](t, body)["name"] != "v" {
		t.Fatalf("add = %d %s", code, body)
	}
	if code, body := r.api(t, "POST", "/api/workspaces", map[string]string{"name": "v"}); code != 409 || errText(t, body) == "" {
		t.Fatalf("duplicate = %d %s", code, body)
	}
	for _, b := range []any{
		map[string]string{"name": "Bad Name"},
		map[string]string{"name": ""},
		map[string]any{"name": "z", "extra": 1},
		"not an object",
	} {
		if code, body := r.api(t, "POST", "/api/workspaces", b); code != 400 || errText(t, body) == "" {
			t.Errorf("body %v = %d %s, want 400 with an error", b, code, body)
		}
	}
	// Bodies over 64 KiB are refused.
	big := map[string]string{"name": "y", "pad": strings.Repeat("x", 65<<10)}
	if code, body := r.api(t, "POST", "/api/workspaces", big); code != 400 || !strings.Contains(errText(t, body), "64 KiB") {
		t.Fatalf("big body = %d %s", code, body)
	}
	if n := len(r.d.State().Workspaces); n != 2 {
		t.Fatalf("workspaces = %d, want 2", n)
	}
}

// The stored path is canonical: symlinks resolved, "." and ".." gone.
func TestAddFolderCanonicalises(t *testing.T) {
	r := newRig(t, nil)
	real := canon(t, filepath.Join(t.TempDir(), "real dir"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks not available:", err)
	}
	in := filepath.Join(link, ".", "sub", "..")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, body := r.api(t, "POST", "/api/workspaces/w/folders", map[string]string{"name": "c", "path": in, "permission_mode": "plan"})
	if code != 201 {
		t.Fatalf("add = %d %s", code, body)
	}
	got := decode[map[string]string](t, body)
	if got["path"] != real || got["name"] != "c" || got["permission_mode"] != "plan" {
		t.Fatalf("response %v, want path %q", got, real)
	}
	c, err := config.Load(r.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, f, ok := c.Folder("w", "c"); !ok || f.Path != real {
		t.Fatalf("saved %+v", f)
	}
}

// A file, a missing path, a relative path, a bad name or a bad permission
// mode is 400; an unknown workspace is 404; a duplicate name is 409.
func TestAddFolderRejectsFile(t *testing.T) {
	r := newRig(t, nil)
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ws   string
		body map[string]string
		want int
	}{
		{"w", map[string]string{"name": "c", "path": file}, 400},
		{"w", map[string]string{"name": "c", "path": filepath.Join(r.dirA, "missing")}, 400},
		{"w", map[string]string{"name": "c", "path": "relative/dir"}, 400},
		{"w", map[string]string{"name": "c", "path": ""}, 400},
		{"w", map[string]string{"name": "C D", "path": r.dirA}, 400},
		{"w", map[string]string{"name": "c", "path": r.dirA, "permission_mode": "yolo"}, 400},
		{"nope", map[string]string{"name": "c", "path": r.dirA}, 404},
		{"w", map[string]string{"name": "a", "path": r.dirA}, 409},
	}
	for _, tc := range cases {
		code, body := r.api(t, "POST", "/api/workspaces/"+tc.ws+"/folders", tc.body)
		if code != tc.want || errText(t, body) == "" {
			t.Errorf("%s %v = %d %s, want %d with an error", tc.ws, tc.body, code, body, tc.want)
		}
	}
	if n := len(r.d.State().Workspaces[0].Folders); n != 2 {
		t.Fatalf("folders = %d after refused adds", n)
	}
}

// Spec 7.7 through the route: the account cswap maps to the path fills an
// empty account (darwin and linux only).
func TestAddFolderRoutePrefillsAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no cswap on windows")
	}
	r := newRigWith(t, func(o *rigOptions) {
		o.cswapPath = "/opt/cswap"
		o.mapped = func(_ context.Context, _, dir string) (string, bool, error) { return "4", true, nil }
	})
	code, body := r.api(t, "POST", "/api/workspaces/w/folders", map[string]string{"name": "c", "path": r.dirB})
	if code != 201 || decode[map[string]string](t, body)["account"] != "4" {
		t.Fatalf("add = %d %s, want account 4", code, body)
	}
	code, body = r.api(t, "POST", "/api/workspaces/w/folders", map[string]string{"name": "d", "path": r.dirB, "account": "1"})
	if code != 201 || decode[map[string]string](t, body)["account"] != "1" {
		t.Fatalf("explicit account = %d %s", code, body)
	}
}

func TestPickFolder(t *testing.T) {
	r := newRig(t, nil)
	r.picker.set("/Users/someone/project", nil)
	code, body := r.api(t, "POST", "/api/pick-folder", nil)
	if code != 200 || body != `{"path":"/Users/someone/project"}`+"\n" {
		t.Fatalf("pick = %d %q", code, body)
	}
	r.picker.set("", ErrPickCancelled)
	code, body = r.api(t, "POST", "/api/pick-folder", nil)
	if code != 200 || body != `{"cancelled":true}`+"\n" {
		t.Fatalf("cancel = %d %q", code, body)
	}
	r.picker.set("", errors.New("no display: secret detail"))
	code, body = r.api(t, "POST", "/api/pick-folder", nil)
	if code != 500 || errText(t, body) == "" || strings.Contains(body, "secret detail") {
		t.Fatalf("dialog error = %d %q", code, body)
	}
	if r.picker.count() != 3 {
		t.Fatalf("picker calls = %d", r.picker.count())
	}
}

func TestSessionsRoute(t *testing.T) {
	r := newRig(t, nil)
	const sid = "11111111-1111-4111-8111-111111111111"
	dir := filepath.Join(r.home, "projects", paths.ProjectDirName(r.dirA))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"hello there"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	code, body := r.api(t, "GET", "/api/workspaces/w/folders/a/sessions", nil)
	if code != 200 || !strings.Contains(body, `"id":"`+sid+`"`) || !strings.Contains(body, `"modified":`) || !strings.Contains(body, `"title":`) {
		t.Fatalf("sessions = %d %s", code, body)
	}
	if code, body := r.api(t, "GET", "/api/workspaces/w/folders/b/sessions", nil); code != 200 || body != `{"sessions":[]}`+"\n" {
		t.Fatalf("empty sessions = %d %s", code, body)
	}
	if code, _ := r.api(t, "GET", "/api/workspaces/w/folders/zz/sessions", nil); code != 404 {
		t.Fatalf("unknown folder = %d", code)
	}
}

func TestMessagesRoutes(t *testing.T) {
	r := newRig(t, nil)
	if code, body := r.api(t, "GET", "/api/workspaces/w/messages", nil); code != 200 || body != `{"messages":[]}`+"\n" {
		t.Fatalf("messages = %d %s", code, body)
	}
	if code, _ := r.api(t, "GET", "/api/workspaces/zz/messages", nil); code != 404 {
		t.Fatalf("unknown ws = %d", code)
	}
	for _, p := range []string{"/api/messages/nope/approve", "/api/messages/nope/reject"} {
		if code, body := r.api(t, "POST", p, nil); code != 404 || errText(t, body) == "" {
			t.Fatalf("%s = %d %s", p, code, body)
		}
	}
	if code, _ := r.api(t, "POST", "/api/agents/w/a/deliver-now", nil); code != 204 {
		t.Fatalf("deliver-now = %d", code)
	}
	if code, _ := r.api(t, "POST", "/api/agents/w/zz/deliver-now", nil); code != 404 {
		t.Fatalf("deliver-now unknown = %d", code)
	}
}

func TestStartTwiceConflict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY is not reliable on CI runners; PTY tests run on unix only")
	}
	t.Setenv("PATH", fakeBinDir)
	r := newRig(t, nil)
	if code, body := r.api(t, "POST", "/api/agents/w/a/start", nil); code != 204 {
		t.Fatalf("start = %d %s", code, body)
	}
	code, body := r.api(t, "POST", "/api/agents/w/a/start", map[string]any{"new": true})
	if code != 409 || !strings.Contains(errText(t, body), "already running") {
		t.Fatalf("second start = %d %s", code, body)
	}
	for b, want := range map[string]int{
		`{"session_id":"NOT-A-UUID"}`:                           400,
		`{"session_id":"22222222-2222-4222-8222-222222222222"}`: 400, // no session file
		`{"new":"yes"}`: 400,
	} {
		code, body := r.raw(t, "POST", "/api/agents/w/b/start", b)
		if code != want || errText(t, body) == "" {
			t.Errorf("start b %s = %d %s, want %d", b, code, body, want)
		}
	}
	if code, _ := r.api(t, "POST", "/api/agents/w/zz/start", nil); code != 404 {
		t.Fatalf("start unknown = %d", code)
	}
}

// Ruling 9: a draft refuses a normal stop with 409 and the daemon's text;
// ?force=true kills it.
func TestStopDraftConflict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY is not reliable on CI runners; PTY tests run on unix only")
	}
	t.Setenv("PATH", fakeBinDir)
	r := newRig(t, nil)
	if code, body := r.api(t, "POST", "/api/agents/w/a/stop", nil); code != 409 || !strings.Contains(errText(t, body), "not running") {
		t.Fatalf("stop not running = %d %s", code, body)
	}
	if code, body := r.api(t, "POST", "/api/agents/w/a/start", nil); code != 204 {
		t.Fatalf("start = %d %s", code, body)
	}
	a, _ := r.d.Agent("w", "a")
	waitFor(t, "input box", 5*time.Second, func() bool { return a.ScreenState().InputVisible })
	if err := a.Write([]byte("half typed")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "draft", 5*time.Second, a.HasDraft)
	code, body := r.api(t, "POST", "/api/agents/w/a/stop", nil)
	if code != 409 || errText(t, body) != "agent has an unsent draft; clear it or use force" {
		t.Fatalf("stop with draft = %d %s", code, body)
	}
	if code, _ := r.api(t, "POST", "/api/agents/w/a/stop?force=maybe", nil); code != 400 {
		t.Fatalf("bad force = %d", code)
	}
	if code, body := r.api(t, "POST", "/api/agents/w/a/stop?force=true", nil); code != 204 {
		t.Fatalf("force stop = %d %s", code, body)
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("agent still running after force stop")
	}
	if strings.Contains(r.lines(a.ID()), "/exit") {
		t.Fatal("force stop typed /exit")
	}
}

// raw sends a literal body with the UI token.
func (r *rig) raw(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+uiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Approve or reject of a message that is not held at the hop limit is 409;
// the timeline lists it with snake_case keys.
func TestApproveQueuedMessageConflict(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	a, _ := r.d.Agent("w", "a")
	if code, body := r.tool(t, r.info(t, a.ID()).Token, "notify", map[string]string{"target": "b", "message": "x"}); code != 200 {
		t.Fatalf("notify = %d %s", code, body)
	}
	code, body := r.api(t, "GET", "/api/workspaces/w/messages", nil)
	msgs := decode[struct {
		Messages []map[string]any `json:"messages"`
	}](t, body).Messages
	if code != 200 || len(msgs) != 1 || msgs[0]["from"] != "a" || msgs[0]["to"] != "b" || msgs[0]["id"] == "" {
		t.Fatalf("messages = %d %s", code, body)
	}
	id := msgs[0]["id"].(string)
	for _, p := range []string{"/api/messages/" + id + "/approve", "/api/messages/" + id + "/reject"} {
		if code, body := r.api(t, "POST", p, nil); code != 409 || !strings.Contains(errText(t, body), "not awaiting_approval") {
			t.Fatalf("%s = %d %s", p, code, body)
		}
	}
}
