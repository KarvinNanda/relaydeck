package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/notify"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type dir map[string][]string

func (d dir) Folders(ws string) []string { return d[ws] }

type tokens map[string]string

func (t tokens) Lookup(tok string) (string, bool) { id, ok := t[tok]; return id, ok }

type daemon struct {
	srv    *httptest.Server
	router *notify.Router
}

func newDaemon(t *testing.T) *daemon {
	t.Helper()
	d := dir{"w1": {"fe", "be", "db"}, "w2": {"other"}}
	n := 0
	r := notify.NewRouter(d, func() time.Time { return time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC) },
		func() string { n++; return "m" + string(rune('0'+n)) })
	who := map[string][2]string{"agent-fe": {"w1", "fe"}, "agent-be": {"w1", "be"}}
	h := Handlers(r, d, func(ws, f string) string { return "idle" },
		func(id string) (string, string, bool) { v, ok := who[id]; return v[0], v[1], ok })
	g := &auth.Guard{Agents: tokens{"tok-fe": "agent-fe", "tok-be": "agent-be", "tok-ghost": "agent-ghost"}}
	srv := httptest.NewServer(g.Agent(h))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	g.Port = portOf(u)
	return &daemon{srv, r}
}

func portOf(u *url.URL) int {
	p := 0
	for _, c := range u.Port() {
		p = p*10 + int(c-'0')
	}
	return p
}

func connect(t *testing.T, d *daemon, token string) *mcp.ClientSession {
	t.Helper()
	env := map[string]string{"RELAYDECK_URL": d.srv.URL, "RELAYDECK_AGENT_TOKEN": token}
	s, err := NewServer(func(k string) string { return env[k] }, nil)
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); cancel(); wg.Wait() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsListed(t *testing.T) {
	cs := connect(t, newDaemon(t), "tok-fe")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "list_folders,notify,read_messages" {
		t.Fatalf("tools = %s", got)
	}
}

func TestListFolders(t *testing.T) {
	cs := connect(t, newDaemon(t), "tok-fe")
	res := call(t, cs, "list_folders", nil)
	want := "be (idle)\ndb (idle)\nfe (idle) (you)"
	if res.IsError || text(res) != want {
		t.Fatalf("got %q err=%v", text(res), res.IsError)
	}
}

func TestNotifyCallsDaemonWithAgentToken(t *testing.T) {
	d := newDaemon(t)
	cs := connect(t, d, "tok-fe")
	res := call(t, cs, "notify", map[string]any{"target": "be", "message": "start the server"})
	if res.IsError || text(res) != `Message m1 queued for "be" (hop 1).` {
		t.Fatalf("got %q err=%v", text(res), res.IsError)
	}
	msgs := d.router.Pending("w1", "be")
	if len(msgs) != 1 || msgs[0].From != "fe" || msgs[0].Body != "start the server" {
		t.Fatalf("stored = %+v", msgs)
	}
}

func TestNotifyHeldAtHopLimit(t *testing.T) {
	d := newDaemon(t)
	cs := connect(t, d, "tok-fe")
	d.router.Send("w1", "db", "be", "a") // hop 1
	d.router.Read("w1", "be")
	d.router.Send("w1", "be", "fe", "b") // hop 2
	d.router.Read("w1", "fe")
	res := call(t, cs, "notify", map[string]any{"target": "be", "message": "go"}) // hop 3
	want := `Message m3 for "be" is held: hop limit reached, waiting for the user to approve.`
	if res.IsError || text(res) != want {
		t.Fatalf("got %q err=%v", text(res), res.IsError)
	}
}

func TestNotifyErrorIsToolError(t *testing.T) {
	cs := connect(t, newDaemon(t), "tok-fe")
	res := call(t, cs, "notify", map[string]any{"target": "nope", "message": "hi"})
	if !res.IsError || !strings.Contains(text(res), `unknown folder "nope"; valid: be, db, fe`) {
		t.Fatalf("got %q err=%v", text(res), res.IsError)
	}
	res = call(t, cs, "notify", map[string]any{"target": "fe", "message": "hi"})
	if !res.IsError || !strings.Contains(text(res), "cannot notify yourself") {
		t.Fatalf("got %q", text(res))
	}
}

func TestReadMessagesEnvelope(t *testing.T) {
	d := newDaemon(t)
	fe := connect(t, d, "tok-fe")
	be := connect(t, d, "tok-be")
	if s := text(call(t, be, "read_messages", nil)); s != "No messages." {
		t.Fatalf("got %q", s)
	}
	call(t, fe, "notify", map[string]any{"target": "be", "message": "first"})
	call(t, fe, "notify", map[string]any{"target": "be", "message": "second"})
	s := text(call(t, be, "read_messages", nil))
	if strings.Count(s, "[relaydeck message] from agent \"fe\"") != 2 ||
		!strings.Contains(s, "not by the user") || !strings.Contains(s, "---\nfirst\n\n[relaydeck message]") ||
		!strings.HasSuffix(s, "---\nsecond") {
		t.Fatalf("got %q", s)
	}
	if s := text(call(t, be, "read_messages", nil)); s != "No messages." {
		t.Fatalf("second read: %q", s)
	}
}

func post(t *testing.T, d *daemon, path, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", d.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func TestAgentCannotSendAsOther(t *testing.T) {
	d := newDaemon(t)
	code, _ := post(t, d, "/agent/tools/notify", "tok-fe",
		`{"target":"db","message":"hi","from":"be","agent":"agent-be","workspace":"w2"}`)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	msgs := d.router.Pending("w1", "db")
	if len(msgs) != 1 || msgs[0].From != "fe" || msgs[0].Workspace != "w1" {
		t.Fatalf("stored = %+v", msgs)
	}
}

func TestHandlerAuthAndErrors(t *testing.T) {
	d := newDaemon(t)
	if code, _ := post(t, d, "/agent/tools/list_folders", "", "{}"); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := post(t, d, "/agent/tools/list_folders", "tok-ghost", "{}"); code != 403 {
		t.Fatalf("unknown agent: %d", code)
	}
	if code, b := post(t, d, "/agent/tools/nope", "tok-fe", "{}"); code != 404 || !strings.Contains(b, `"error"`) {
		t.Fatalf("unknown tool: %d %s", code, b)
	}
	if code, _ := post(t, d, "/agent/tools/notify", "tok-fe", "not json"); code != 400 {
		t.Fatalf("bad json: %d", code)
	}
	big := `{"target":"be","message":"` + strings.Repeat("a", 140<<10) + `"}`
	if code, b := post(t, d, "/agent/tools/notify", "tok-fe", big); code != 400 || !strings.Contains(b, "request body over 128 KiB") {
		t.Fatalf("oversize: %d %s", code, b)
	}
}

func TestRunConfigErrors(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for name, env := range map[string]map[string]string{
		"remote host": {"RELAYDECK_URL": "http://example.com:7341", "RELAYDECK_AGENT_TOKEN": "x"},
		"https":       {"RELAYDECK_URL": "https://127.0.0.1:7341", "RELAYDECK_AGENT_TOKEN": "x"},
		"no port":     {"RELAYDECK_URL": "http://127.0.0.1", "RELAYDECK_AGENT_TOKEN": "x"},
		"no token":    {"RELAYDECK_URL": "http://127.0.0.1:7341"},
	} {
		if _, err := NewServer(get(env), nil); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := NewServer(get(map[string]string{"RELAYDECK_URL": "http://localhost:7341", "RELAYDECK_AGENT_TOKEN": "x"}), nil); err != nil {
		t.Errorf("localhost: %v", err)
	}
}

func TestNoRedirectAndTokenNotLeaked(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	env := map[string]string{"RELAYDECK_URL": srv.URL, "RELAYDECK_AGENT_TOKEN": "secret-tok"}
	s, _ := NewServer(func(k string) string { return env[k] }, nil)
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "v"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cs.Close(); cancel(); <-done }()
	res := call(t, cs, "list_folders", nil)
	if !res.IsError || hits.Load() != 0 || strings.Contains(text(res), "secret-tok") {
		t.Fatalf("err=%v hits=%d text=%q", res.IsError, hits.Load(), text(res))
	}
}

func TestNotifyOtherWorkspaceRejected(t *testing.T) {
	d := newDaemon(t)
	cs := connect(t, d, "tok-fe")
	res := call(t, cs, "notify", map[string]any{"target": "other", "message": "hi"})
	if !res.IsError || !strings.Contains(text(res), `unknown folder "other"`) {
		t.Fatalf("got %q err=%v", text(res), res.IsError)
	}
	if len(d.router.Pending("w2", "other")) != 0 {
		t.Fatal("message crossed workspaces")
	}
}

func TestReadMessagesOnlyOwn(t *testing.T) {
	d := newDaemon(t)
	fe := connect(t, d, "tok-fe")
	if _, err := d.router.Send("w1", "fe", "db", "for db"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.router.Send("w1", "db", "be", "for be"); err != nil {
		t.Fatal(err)
	}
	if s := text(call(t, fe, "read_messages", nil)); s != "No messages." {
		t.Fatalf("fe read %q", s)
	}
	if len(d.router.Pending("w1", "db")) != 1 || len(d.router.Pending("w1", "be")) != 1 {
		t.Fatal("another agent's messages were consumed")
	}
}

func TestReadMessagesLargeBacklogNoLoss(t *testing.T) {
	d := newDaemon(t)
	fe := connect(t, d, "tok-fe")
	be := connect(t, d, "tok-be")
	const total, size = 80, 16 << 10
	sent, seen, bytes := 0, map[int]int{}, 0
	for len(seen) < total {
		for sent < total { // refill up to the pending cap
			body := fmt.Sprintf("msg-%03d-", sent) + strings.Repeat("z", size-8)
			res := call(t, fe, "notify", map[string]any{"target": "be", "message": body})
			if res.IsError {
				break
			}
			sent++
		}
		res := call(t, be, "read_messages", nil)
		if res.IsError {
			t.Fatalf("read error %q", text(res))
		}
		s := text(res)
		if len(s) > 300<<10 {
			t.Fatalf("answer too large: %d", len(s))
		}
		bytes += len(s)
		for i := 0; i < total; i++ {
			seen[i] += strings.Count(s, fmt.Sprintf("---\nmsg-%03d-", i))
		}
		delete(seen, -1)
		for k, v := range seen {
			if v == 0 {
				delete(seen, k)
			}
		}
	}
	for i := 0; i < total; i++ {
		if seen[i] != 1 {
			t.Fatalf("message %d seen %d times", i, seen[i])
		}
	}
	if bytes <= 1<<20 {
		t.Fatalf("test did not exceed 1 MiB: %d", bytes)
	}
	if s := text(call(t, be, "read_messages", nil)); s != "No messages." {
		t.Fatalf("leftover %q", s)
	}
}

func TestClientRejectsOversizeAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"text":"` + strings.Repeat("a", 2<<20) + `"}`))
	}))
	defer srv.Close()
	env := map[string]string{"RELAYDECK_URL": srv.URL, "RELAYDECK_AGENT_TOKEN": "t"}
	s, _ := NewServer(func(k string) string { return env[k] }, nil)
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "v"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cs.Close(); cancel(); <-done }()
	res := call(t, cs, "read_messages", nil)
	if !res.IsError || text(res) != "daemon response unreadable or too large" {
		t.Fatalf("got %q err=%v", text(res), res.IsError)
	}
}

func TestReadMessagesEscapeHeavyNoLoss(t *testing.T) {
	d := newDaemon(t)
	fe := connect(t, d, "tok-fe")
	be := connect(t, d, "tok-be")
	const total = 50
	for i := 0; i < total; i++ {
		// Control characters are valid UTF-8 but JSON-escape to 6 bytes each.
		body := fmt.Sprintf("%02d", i) + strings.Repeat("\x01", 16<<10-2)
		if res := call(t, fe, "notify", map[string]any{"target": "be", "message": body}); res.IsError {
			t.Fatalf("notify %d: %q", i, text(res))
		}
	}
	got := 0
	for calls := 0; got < total; calls++ {
		if calls > total {
			t.Fatal("too many calls")
		}
		res := call(t, be, "read_messages", nil)
		if res.IsError {
			t.Fatalf("read error after %d messages: %q", got, text(res))
		}
		got += strings.Count(text(res), "[relaydeck message] from agent")
	}
	if got != total {
		t.Fatalf("got %d messages, want %d", got, total)
	}
	if s := text(call(t, be, "read_messages", nil)); s != "No messages." {
		t.Fatalf("leftover %q", s)
	}
}
