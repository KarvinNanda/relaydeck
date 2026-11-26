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

	"github.com/KarvinNanda/relaydeck/internal/attach"
	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/daemon"
	"github.com/KarvinNanda/relaydeck/internal/launch"
	"github.com/KarvinNanda/relaydeck/internal/notify"
	"github.com/KarvinNanda/relaydeck/internal/paths"
)

type fakePicker struct {
	mu    sync.Mutex
	calls int
	path  string
	err   error
	block chan struct{} // when set, PickFolder waits for it to close
}

func (p *fakePicker) PickFolder(context.Context) (string, error) {
	p.mu.Lock()
	p.calls++
	path, err, block := p.path, p.err, p.block
	p.mu.Unlock()
	if block != nil {
		<-block
	}
	return path, err
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
	{"POST", "/api/agents/w/a/attachments"},
	{"POST", "/api/ws-ticket"},
	// last: with the token these really remove
	{"DELETE", "/api/workspaces/w/folders/b"},
	{"DELETE", "/api/workspaces/w"},
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
	if st := r.d.State(); len(st.Workspaces) != 1 || len(st.Workspaces[0].Folders) != 2 {
		t.Fatalf("refused request changed the workspaces: %+v", st.Workspaces)
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

// #43: the 400 for a bad path has fixed text. It must not echo the resolved
// path or the raw error of the file system.
func TestAddFolderBadPathHasFixedText(t *testing.T) {
	r := newRig(t, nil)
	link := filepath.Join(t.TempDir(), "link")
	missing := filepath.Join(r.dirA, "no-such-dir")
	if err := os.Symlink(missing, link); err != nil {
		t.Skip("symlink not available")
	}
	for _, p := range []string{missing, link} {
		code, body := r.api(t, "POST", "/api/workspaces/w/folders", map[string]string{"name": "c", "path": p})
		if code != 400 {
			t.Fatalf("code %d %s", code, body)
		}
		for _, bad := range []string{r.dirA, "no-such-dir", "no such file", "lstat", "stat "} {
			if strings.Contains(body, bad) {
				t.Errorf("body leaks %q: %s", bad, body)
			}
		}
		if !strings.Contains(errText(t, body), "existing directory") {
			t.Errorf("unexpected text: %s", body)
		}
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
	code, body := r.api(t, "POST", "/api/workspaces/w/folders", map[string]string{"name": "c", "path": r.dirC})
	if code != 201 || decode[map[string]string](t, body)["account"] != "4" {
		t.Fatalf("add = %d %s, want account 4", code, body)
	}
	code, body = r.api(t, "POST", "/api/workspaces/w/folders", map[string]string{"name": "d", "path": r.dirD, "account": "1"})
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

// Shutting down and a missing claude are 503 with a fixed text.
func TestStartErrorStatuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH lookup differs on windows")
	}
	noAgents(t)
	r := newRig(t, nil)
	code, body := r.api(t, "POST", "/api/agents/w/a/start", nil)
	if code != 503 || errText(t, body) != "claude is not installed or not on PATH" {
		t.Fatalf("claude missing = %d %s", code, body)
	}
	r.stop()
	code, body = r.api(t, "POST", "/api/agents/w/a/start", nil)
	if code != 503 || !strings.Contains(errText(t, body), "shutting down") {
		t.Fatalf("shutting down = %d %s", code, body)
	}
}

func TestStatusOfNewSentinels(t *testing.T) {
	if got := statusOf(fmt.Errorf("%w: ps failed", daemon.ErrExternalCheck)); got != 503 {
		t.Fatalf("ErrExternalCheck = %d, want 503", got)
	}
	if got := statusOf(fmt.Errorf("%w: session id", launch.ErrBadInput)); got != 400 {
		t.Fatalf("launch.ErrBadInput = %d, want 400", got)
	}
	if got := statusOf(fmt.Errorf("%w: stop a first", daemon.ErrRemoveRunning)); got != 409 {
		t.Fatalf("ErrRemoveRunning = %d, want 409", got)
	}
	if got := statusOf(daemon.ErrShuttingDown); got != 503 {
		t.Fatalf("ErrShuttingDown = %d, want 503", got)
	}
}

// #116: DELETE removes a folder or a whole workspace from the config (204).
// Unknown is 404, a running agent is 409 with the folders to stop.
func TestRemoveRoutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY is not reliable on CI runners; PTY tests run on unix only")
	}
	t.Setenv("PATH", fakeBinDir)
	r := newRig(t, nil)
	if code, body := r.api(t, "DELETE", "/api/workspaces/w/folders/b", nil); code != 204 {
		t.Fatalf("remove b = %d %s", code, body)
	}
	if code, body := r.api(t, "DELETE", "/api/workspaces/w/folders/b", nil); code != 404 || errText(t, body) == "" {
		t.Fatalf("remove b again = %d %s", code, body)
	}
	if code, _ := r.api(t, "DELETE", "/api/workspaces/nope", nil); code != 404 {
		t.Fatalf("remove unknown workspace = %d", code)
	}
	if code, body := r.api(t, "POST", "/api/agents/w/a/start", nil); code != 204 {
		t.Fatalf("start = %d %s", code, body)
	}
	for _, path := range []string{"/api/workspaces/w/folders/a", "/api/workspaces/w"} {
		code, body := r.api(t, "DELETE", path, nil)
		if code != 409 || errText(t, body) != "a running agent cannot be removed: stop a first" {
			t.Fatalf("DELETE %s while a runs = %d %s", path, code, body)
		}
	}
	if code, body := r.api(t, "POST", "/api/agents/w/a/stop?force=true", nil); code != 204 {
		t.Fatalf("stop = %d %s", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := r.api(t, "DELETE", "/api/workspaces/w", nil)
		if code == 204 {
			break
		}
		if code != 409 || time.Now().After(deadline) {
			t.Fatalf("remove w after stop = %d %s", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(r.d.State().Workspaces); n != 0 {
		t.Fatalf("workspaces left = %d, want 0", n)
	}
}

// A failed external session check is 503 with a fixed text. The cause (here
// a path) goes to the log with the request_id, never to the client.
func TestStartExternalCheckFailureHidesCause(t *testing.T) {
	const cur = "11111111-1111-4111-8111-111111111111"
	r := newRigWith(t, func(o *rigOptions) {
		o.external = func(context.Context, string, string, map[int]bool) (bool, error) {
			return false, errors.New("ps: cannot read /Users/someone/secret")
		}
	})
	// The session needs a transcript, or the start stops before the check.
	sessDir := filepath.Join(r.home, "projects", paths.ProjectDirName(r.dirA))
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, cur+".jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, body := r.api(t, "POST", "/api/agents/w/a/start", map[string]any{"session_id": cur})
	if code != 503 || errText(t, body) != "could not check for an open Claude Code session in this folder" {
		t.Fatalf("external check failure = %d %s", code, body)
	}
	if strings.Contains(body, "/Users/someone/secret") {
		t.Fatalf("cause leaked to the client: %s", body)
	}
	if logs := r.logs.String(); !strings.Contains(logs, "/Users/someone/secret") || !strings.Contains(logs, "api unavailable") {
		t.Fatalf("cause not logged: %s", logs)
	}
}

func TestAddFolderRejectsBadAccount(t *testing.T) {
	r := newRig(t, nil)
	for _, acct := range []string{"-x", "a b", "a;b", "$(id)"} {
		code, body := r.api(t, "POST", "/api/workspaces/w/folders", map[string]string{"name": "c", "path": r.dirA, "account": acct})
		if code != 400 || !strings.Contains(errText(t, body), "account") {
			t.Errorf("account %q = %d %s, want 400 naming the account", acct, code, body)
		}
	}
	if n := len(r.d.State().Workspaces[0].Folders); n != 2 {
		t.Fatalf("folders = %d after refused adds", n)
	}
}

// Only one folder dialog at a time: a second request while one is open is
// 409 at once, and the picker is called once.
func TestPickFolderBusy(t *testing.T) {
	r := newRig(t, nil)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock) // a failed assertion must not leave the dialog blocked
	r.picker.block = release
	first := make(chan int, 1)
	go func() {
		code, _ := r.api(t, "POST", "/api/pick-folder", nil)
		first <- code
	}()
	waitFor(t, "first dialog open", 5*time.Second, func() bool { return r.picker.count() == 1 })
	code, body := r.api(t, "POST", "/api/pick-folder", nil)
	if code != 409 || !strings.Contains(errText(t, body), "already open") {
		t.Fatalf("second dialog = %d %s", code, body)
	}
	unblock()
	if c := <-first; c != 200 {
		t.Fatalf("first dialog = %d", c)
	}
	if r.picker.count() != 1 {
		t.Fatalf("picker calls = %d", r.picker.count())
	}
	// The lock is free again.
	r.picker.block = nil
	if code, _ := r.api(t, "POST", "/api/pick-folder", nil); code != 200 {
		t.Fatalf("dialog after release = %d", code)
	}
}

// Anything after the first JSON value is a 400, including a stray bracket
// that json.Decoder.More would let through.
func TestReadBodyRejectsTrailingData(t *testing.T) {
	r := newRig(t, nil)
	for _, b := range []string{`{"name":"x"} {"name":"y"}`, `{"name":"x"}x`, `{"name":"x"}}`, `{"name":"x"}]`, `{"name":"x"} 1`} {
		code, body := r.raw(t, "POST", "/api/workspaces", b)
		if code != 400 || !strings.Contains(errText(t, body), "extra data") {
			t.Errorf("body %q = %d %s, want 400 extra data", b, code, body)
		}
	}
	if n := len(r.d.State().Workspaces); n != 1 {
		t.Fatalf("workspaces = %d after refused bodies", n)
	}
	if code, _ := r.raw(t, "POST", "/api/workspaces", `{"name":"ok"}  `+"\n"); code != 201 {
		t.Fatalf("body with trailing whitespace = %d, want 201", code)
	}
}

// A message held at the hop limit is released by approve (204), and a second
// approve is 409. The chain a -> b -> a -> b puts the third message at hop 3.
func TestApproveHeldMessage(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	startAgent(t, r, "b")
	aa, _ := r.d.Agent("w", "a")
	ab, _ := r.d.Agent("w", "b")
	ta, tb := r.info(t, aa.ID()).Token, r.info(t, ab.ID()).Token
	send := func(token, to string) {
		t.Helper()
		if code, body := r.tool(t, token, "notify", map[string]string{"target": to, "message": "x"}); code != 200 {
			t.Fatalf("notify %s = %d %s", to, code, body)
		}
	}
	read := func(token string) {
		t.Helper()
		if code, body := r.tool(t, token, "read_messages", nil); code != 200 {
			t.Fatalf("read_messages = %d %s", code, body)
		}
	}
	send(ta, "b")
	read(tb)
	send(tb, "a")
	read(ta)
	send(ta, "b")
	var held notify.Message
	msgs, _ := r.d.Messages("w")
	for _, m := range msgs {
		if m.State == notify.StateAwaitingApproval {
			held = m
		}
	}
	if held.ID == "" {
		t.Fatalf("no message held at the hop limit: %+v", msgs)
	}
	if code, body := r.api(t, "POST", "/api/messages/"+held.ID+"/approve", nil); code != 204 || body != "" {
		t.Fatalf("approve = %d %q", code, body)
	}
	if got := r.message(t, held.ID).State; got == notify.StateAwaitingApproval {
		t.Fatalf("message still %s after approve", got)
	}
	if code, body := r.api(t, "POST", "/api/messages/"+held.ID+"/approve", nil); code != 409 {
		t.Fatalf("second approve = %d %s, want 409", code, body)
	}
}

// An image upload is saved under the running agent's runtime dir and its path
// is returned; anything else is refused with a fixed text and leaves no file.
func TestAttachmentRoute(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	png := "\x89PNG\x0D\x0A\x1A\x0A" + strings.Repeat("\x00", 2000)

	if code, body := r.raw(t, "POST", "/api/agents/w/a/attachments", png); code != 409 || strings.Contains(errText(t, body), "/") {
		t.Fatalf("not running = %d %s", code, body)
	}
	if code, _ := r.raw(t, "POST", "/api/agents/w/zz/attachments", png); code != 404 {
		t.Fatalf("unknown folder = %d", code)
	}

	startAgent(t, r, "a")
	code, body := r.raw(t, "POST", "/api/agents/w/a/attachments", png)
	if code != 200 {
		t.Fatalf("png = %d %s", code, body)
	}
	p := decode[map[string]string](t, body)["path"]
	dir, err := r.d.AttachmentDir("w", "a")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != dir || !strings.HasSuffix(p, ".png") {
		t.Fatalf("path %q not a .png in %q", p, dir)
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != png {
		t.Fatalf("saved bytes differ (%v)", err)
	}

	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"svg", `<svg xmlns="http://www.w3.org/2000/svg"></svg>`, 415},
		{"text", "hello", 415},
		{"too large", "\x89PNG\x0D\x0A\x1A\x0A" + strings.Repeat("\x00", int(attach.MaxFile)-7), 413},
	} {
		code, body := r.raw(t, "POST", "/api/agents/w/a/attachments", c.body)
		if code != c.want || strings.Contains(errText(t, body), "/") {
			t.Fatalf("%s = %d %s, want %d", c.name, code, body, c.want)
		}
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Fatalf("attachments dir holds %d files, want 1", len(es))
	}
}

// The agent's runtime dir is gone (it exited between the lookup and the
// save): the route answers 409 and does not create the dir again (#73).
func TestAttachmentRouteRuntimeDirGone(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	dir, err := r.d.AttachmentDir("w", "a")
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Dir(dir)
	if err := os.RemoveAll(runtimeDir); err != nil {
		t.Fatal(err)
	}
	png := "\x89PNG\x0D\x0A\x1A\x0A" + strings.Repeat("\x00", 100)
	code, body := r.raw(t, "POST", "/api/agents/w/a/attachments", png)
	if code != 409 || errText(t, body) != daemon.ErrNotRunning.Error() {
		t.Fatalf("runtime dir gone = %d %s, want 409 %q", code, body, daemon.ErrNotRunning.Error())
	}
	if _, err := os.Stat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime dir was created again: %v", err)
	}
}

// The agent exits while the upload body is still being read: the route must
// answer 409 and leave no file behind (#72 review).
func TestAttachmentRouteAgentExitsMidUpload(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	dir, err := r.d.AttachmentDir("w", "a")
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	req, err := http.NewRequest("POST", r.srv.URL+"/api/agents/w/a/attachments", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+uiToken)
	type answer struct {
		code int
		body string
	}
	got := make(chan answer, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- answer{0, err.Error()}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- answer{resp.StatusCode, string(b)}
	}()
	png := "\x89PNG\x0D\x0A\x1A\x0A" + strings.Repeat("\x00", 1000)
	if _, err := pw.Write([]byte(png)); err != nil {
		t.Fatal(err)
	}
	a, _ := r.d.Agent("w", "a")
	if code, body := r.api(t, "POST", "/api/agents/w/a/stop?force=true", nil); code != 204 {
		t.Fatalf("stop = %d %s", code, body)
	}
	<-a.Done()
	waitFor(t, "runtime dir removed", 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Dir(dir))
		return errors.Is(err, os.ErrNotExist)
	})
	pw.Write([]byte(strings.Repeat("\x00", 1000)))
	pw.Close()
	ans := <-got
	if ans.code != 409 || errText(t, ans.body) != daemon.ErrNotRunning.Error() {
		t.Fatalf("upload during exit = %d %s, want 409", ans.code, ans.body)
	}
	if _, err := os.Stat(filepath.Dir(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime dir exists again: %v", err)
	}
}
