package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func (r *rig) ticket(t *testing.T) string {
	t.Helper()
	code, body := r.api(t, "POST", "/api/ws-ticket", nil)
	if code != 200 {
		t.Fatalf("ws-ticket = %d %s", code, body)
	}
	tk := decode[map[string]string](t, body)["ticket"]
	if tk == "" {
		t.Fatalf("no ticket in %s", body)
	}
	return tk
}

func (r *rig) origin() string { return fmt.Sprintf("http://127.0.0.1:%d", r.port) }

// dial opens a WebSocket; ticket and origin are left out when empty.
func (r *rig) dial(t *testing.T, path, ticket, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u := fmt.Sprintf("ws://127.0.0.1:%d%s", r.port, path)
	if ticket != "" {
		u += "?ticket=" + ticket
	}
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h})
	if c != nil {
		t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

func (r *rig) mustDial(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	c, resp, err := r.dial(t, path, r.ticket(t), r.origin())
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (status %d)", path, err, code)
	}
	return c
}

func status(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

// readUntil reads binary frames until their joined bytes contain want.
func readUntil(t *testing.T, c *websocket.Conn, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var all bytes.Buffer
	for !strings.Contains(all.String(), want) {
		typ, p, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v; got %q", want, err, all.String())
		}
		if typ != websocket.MessageBinary {
			t.Fatalf("terminal sent a %v frame", typ)
		}
		all.Write(p)
	}
	return all.String()
}

func sendJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func input(t *testing.T, c *websocket.Conn, s string) {
	t.Helper()
	sendJSON(t, c, map[string]string{"type": "input", "data": s})
}

func closeStatus(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("socket not closed in time")
			}
			return websocket.CloseStatus(err)
		}
	}
}

func startAgent(t *testing.T, r *rig, folder string) {
	t.Helper()
	if code, body := r.api(t, "POST", "/api/agents/w/"+folder+"/start", nil); code != 204 {
		t.Fatalf("start = %d %s", code, body)
	}
	a, _ := r.d.Agent("w", folder)
	waitFor(t, "input box", 5*time.Second, func() bool { return a.ScreenState().InputVisible })
}

func needPTY(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY is not reliable on CI runners; PTY tests run on unix only")
	}
	t.Setenv("PATH", fakeBinDir)
}

// A WebSocket needs a ticket from POST /api/ws-ticket (which needs the UI
// token), the ticket works once, and the Origin header is required.
func TestWSRequiresTicketOnce(t *testing.T) {
	r := newRig(t, nil)
	if code, _ := r.do(t, "POST", "/api/ws-ticket", "", "", nil); code != 401 {
		t.Fatalf("ticket without token = %d", code)
	}
	if _, resp, err := r.dial(t, "/ws/events", "", r.origin()); err == nil || status(resp) != 401 {
		t.Fatalf("no ticket: err %v status %d, want 401", err, status(resp))
	}
	if _, resp, err := r.dial(t, "/ws/events", uiToken, r.origin()); err == nil || status(resp) != 401 {
		t.Fatalf("UI token as ticket: err %v status %d, want 401", err, status(resp))
	}
	tk := r.ticket(t)
	if _, resp, err := r.dial(t, "/ws/events", tk, ""); err == nil || status(resp) != 403 {
		t.Fatalf("no origin: err %v status %d, want 403", err, status(resp))
	}
	tk = r.ticket(t)
	if _, resp, err := r.dial(t, "/ws/events", tk, "http://evil.example"); err == nil || status(resp) != 403 {
		t.Fatalf("evil origin: err %v status %d, want 403", err, status(resp))
	}
	tk = r.ticket(t)
	c, resp, err := r.dial(t, "/ws/events", tk, fmt.Sprintf("http://localhost:%d", r.port))
	if err != nil {
		t.Fatalf("first use: %v status %d", err, status(resp))
	}
	c.Close(websocket.StatusNormalClosure, "")
	if _, resp, err := r.dial(t, "/ws/events", tk, r.origin()); err == nil || status(resp) != 401 {
		t.Fatalf("second use: err %v status %d, want 401", err, status(resp))
	}
	if strings.Contains(r.logs.String(), tk) {
		t.Fatal("ticket in the log")
	}
}

// Ruling 11: the first event is a full state snapshot, then live events.
func TestWSEventsStateFirst(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	c := r.mustDial(t, "/ws/events")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, p, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageText {
		t.Fatalf("first frame: %v %v", typ, err)
	}
	var first struct {
		Type       string `json:"type"`
		Platform   string `json:"platform"`
		Workspaces []struct {
			Name    string `json:"name"`
			Folders []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"folders"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(p, &first); err != nil {
		t.Fatal(err)
	}
	if first.Type != "state" || first.Platform != runtime.GOOS || len(first.Workspaces) != 1 || len(first.Workspaces[0].Folders) != 2 {
		t.Fatalf("first frame = %s", p)
	}

	startAgent(t, r, "a")
	for {
		_, p, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ev map[string]any
		if err := json.Unmarshal(p, &ev); err != nil {
			t.Fatal(err)
		}
		if ev["type"] == "status" && ev["ws"] == "w" && ev["folder"] == "a" {
			break
		}
	}
}

// On shutdown (Run returns) the events socket is closed with "going away".
func TestWSEventsClosedOnShutdown(t *testing.T) {
	r := newRig(t, nil)
	c := r.mustDial(t, "/ws/events")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err != nil { // the snapshot
		t.Fatal(err)
	}
	r.stop()
	if got := closeStatus(t, c); got != websocket.StatusGoingAway {
		t.Fatalf("close status = %v, want going away", got)
	}
}

func TestWSTermUnknownAgent(t *testing.T) {
	r := newRig(t, nil)
	for _, p := range []string{"/ws/term/w/a", "/ws/term/w/zz", "/ws/term/zz/a"} {
		if _, resp, err := r.dial(t, p, r.ticket(t), r.origin()); err == nil || status(resp) != 404 {
			t.Errorf("%s: err %v status %d, want 404", p, err, status(resp))
		}
	}
}

// The replay comes first, typed input reaches the PTY and is echoed back,
// and resize is clamped to at least 60x10 (the nudge must fit one line).
func TestWSTermEchoAndResize(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	a, _ := r.d.Agent("w", "a")
	tk := r.ticket(t)
	c, resp, err := r.dial(t, "/ws/term/w/a", tk, r.origin())
	if err != nil {
		t.Fatalf("dial: %v %d", err, status(resp))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, replay, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || !strings.Contains(string(replay), "? for shortcuts") {
		t.Fatalf("replay frame: %v %v %q", typ, err, replay)
	}

	// Junk frames are ignored; the socket stays open.
	if err := c.Write(ctx, websocket.MessageText, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	sendJSON(t, c, map[string]any{"type": "dance"})

	input(t, c, "hello\r")
	readUntil(t, c, "hello")
	waitFor(t, "input line", 5*time.Second, func() bool { return strings.Contains(r.lines(a.ID()), "hello\n") })

	sendJSON(t, c, map[string]any{"type": "resize", "cols": 30, "rows": 5})
	input(t, c, "size\r")
	readUntil(t, c, "SIZE:60x10")

	sendJSON(t, c, map[string]any{"type": "resize", "cols": 100, "rows": 30})
	input(t, c, "size\r")
	readUntil(t, c, "SIZE:100x30")

	if logs := r.logs.String(); strings.Contains(logs, "hello") || strings.Contains(logs, tk) {
		t.Fatalf("typed input or ticket in the log: %s", logs)
	}
	if !strings.Contains(r.logs.String(), `"msg":"terminal attached"`) {
		t.Fatalf("no attach log line: %s", r.logs.String())
	}
}

// Ruling 10: for 1 s after the replay, input frames that are only terminal
// query replies (xterm.js answering queries inside the old scrollback) are
// dropped. Other input in that second passes, and after it replies pass too.
func TestWSTermDropsQueryRepliesAfterReplay(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	a, _ := r.d.Agent("w", "a")
	c := r.mustDial(t, "/ws/term/w/a")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	replayAt := time.Now()

	input(t, c, "\x1b[12;40R")
	input(t, c, "\x1b[?1;2c")
	input(t, c, "\x1b[0n")
	input(t, c, "\x1b[1;1R\x1b[?6c")
	input(t, c, "hi\r")
	waitFor(t, "first line", 5*time.Second, func() bool { return strings.Contains(r.lines(a.ID()), "hi\n") })
	if got := r.lines(a.ID()); got != "hi\n" {
		t.Fatalf("lines = %q, want the query replies dropped", got)
	}
	if time.Since(replayAt) > 900*time.Millisecond {
		t.Fatalf("test too slow to prove the window (%v)", time.Since(replayAt))
	}

	time.Sleep(time.Until(replayAt.Add(1100 * time.Millisecond)))
	input(t, c, "\x1b[3;3R")
	input(t, c, "yo\r")
	waitFor(t, "second line", 5*time.Second, func() bool { return strings.Contains(r.lines(a.ID()), "yo\n") })
	if got := r.lines(a.ID()); got != "hi\n\x1b[3;3Ryo\n" {
		t.Fatalf("lines = %q, want the reply kept after 1 s", got)
	}
}

// When the agent stops, its terminal socket is closed (normal closure), and
// a stopped agent's terminal still shows its scrollback, then closes.
func TestWSTermClosesWhenAgentStops(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	c := r.mustDial(t, "/ws/term/w/a")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if code, body := r.api(t, "POST", "/api/agents/w/a/stop?force=true", nil); code != 204 {
		t.Fatalf("stop = %d %s", code, body)
	}
	if got := closeStatus(t, c); got != websocket.StatusNormalClosure {
		t.Fatalf("close status = %v, want normal", got)
	}

	c2 := r.mustDial(t, "/ws/term/w/a")
	typ, p, err := c2.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || !strings.Contains(string(p), "? for shortcuts") {
		t.Fatalf("stopped agent replay: %v %v %q", typ, err, p)
	}
	if got := closeStatus(t, c2); got != websocket.StatusNormalClosure {
		t.Fatalf("close status = %v, want normal", got)
	}
}

func TestClamp(t *testing.T) {
	for _, tc := range []struct{ v, lo, hi, want int }{
		{5, 10, 100, 10},
		{10, 10, 100, 10},
		{50, 10, 100, 50},
		{100, 10, 100, 100},
		{5000, 10, 100, 100},
		{-3, 10, 100, 10},
	} {
		if got := clamp(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}

// A resize above the maximum is cut to maxCols x maxRows, not passed on.
func TestWSTermResizeClampsToMax(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	c := r.mustDial(t, "/ws/term/w/a")
	sendJSON(t, c, map[string]any{"type": "resize", "cols": 99999, "rows": 99999})
	input(t, c, "size\r")
	readUntil(t, c, fmt.Sprintf("SIZE:%dx%d", maxCols, maxRows))
}

// A frame over the 64 KiB limit closes the socket with 1009 (message too
// big), and nothing of it reaches the agent.
func TestWSTermOversizeFrameClosesWith1009(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	a, _ := r.d.Agent("w", "a")
	c := r.mustDial(t, "/ws/term/w/a")
	big := strings.Repeat("A", maxBody+1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The server may close while the frame is still being written.
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"`+big+`\r"}`))
	if got := closeStatus(t, c); got != websocket.StatusMessageTooBig {
		t.Fatalf("close status = %d, want 1009", got)
	}
	if strings.Contains(r.lines(a.ID()), "AAAA") {
		t.Fatal("oversize frame reached the agent")
	}
}

// An agent token is only for the agent endpoints: every /api route refuses
// it with 401, and it is not a WebSocket ticket either.
func TestAgentTokenRefusedOnUIRoutes(t *testing.T) {
	needPTY(t)
	r := newRig(t, nil)
	startAgent(t, r, "a")
	a, _ := r.d.Agent("w", "a")
	tok := r.info(t, a.ID()).Token
	// Positive control: the token is real and works on an agent endpoint.
	if tok == "" {
		t.Fatal("no agent token")
	}
	if code, body := r.tool(t, tok, "list_folders", nil); code != 200 {
		t.Fatalf("agent token on list_folders = %d %s, want 200", code, body)
	}
	for _, rt := range apiRoutes {
		if code, body := r.do(t, rt.method, rt.path, "", tok, nil); code != 401 {
			t.Errorf("%s %s with an agent token = %d %s, want 401", rt.method, rt.path, code, body)
		}
	}
	for _, p := range []string{"/ws/events", "/ws/term/w/a"} {
		if _, resp, err := r.dial(t, p, tok, r.origin()); err == nil || status(resp) != 401 {
			t.Errorf("agent token as ticket on %s: err %v status %d, want 401", p, err, status(resp))
		}
	}
	if len(r.d.State().Workspaces[0].Folders) != 2 {
		t.Fatal("a refused request changed the state")
	}
}
