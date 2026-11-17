package hook

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KarvinNanda/relaydeck/internal/auth"
)

const goodID = "0b6c1a52-7d3e-4f10-9a8b-1c2d3e4f5a6b"

type fakeAgents struct{}

func (fakeAgents) Lookup(t string) (string, bool) { return "a1", t == "tok" }

func guarded(h http.Handler) http.Handler {
	g := &auth.Guard{Port: 7341, Agents: fakeAgents{}}
	return g.Agent(h)
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "http://127.0.0.1:7341/agent/hook", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRunForwardsOnlyKnownFields(t *testing.T) {
	type seen struct{ body, authz string }
	ch := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- seen{string(b), r.Header.Get("Authorization")}
		if r.URL.Path != "/agent/hook" || r.Method != "POST" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	env := map[string]string{"RELAYDECK_URL": srv.URL, "RELAYDECK_AGENT_TOKEN": "tok"}
	stdin := `{"session_id":"` + goodID + `","prompt":"secret","transcript_path":"/x/secret.jsonl","cwd":"/x","source":"resume"}`
	if err := Run("SessionStart", strings.NewReader(stdin), func(k string) string { return env[k] }, srv.Client()); err != nil {
		t.Fatal(err)
	}
	sn := <-ch
	got := sn.body
	if strings.Contains(got, "secret") || strings.Contains(got, "/x") {
		t.Errorf("body leaks fields: %s", got)
	}
	if !strings.Contains(got, `"event":"SessionStart"`) || !strings.Contains(got, goodID) || !strings.Contains(got, `"source":"resume"`) {
		t.Errorf("body = %s", got)
	}
	if sn.authz != "Bearer tok" {
		t.Errorf("authz = %q", sn.authz)
	}
}

func TestRunNoEnvIsNoop(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	if err := Run("Stop", strings.NewReader("{}"), func(string) string { return "" }, srv.Client()); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Error("request sent without RELAYDECK_URL")
	}
}

func TestRunRejectsUnknownEvent(t *testing.T) {
	if err := Run("Bogus", strings.NewReader("{}"), func(string) string { return "x" }, nil); err == nil {
		t.Error("want error")
	}
}

func TestRunNon2xxErrorHasNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	env := map[string]string{"RELAYDECK_URL": srv.URL, "RELAYDECK_AGENT_TOKEN": "supersecrettoken"}
	err := Run("Stop", strings.NewReader("{}"), func(k string) string { return env[k] }, srv.Client())
	if err == nil || strings.Contains(err.Error(), "supersecrettoken") {
		t.Errorf("err = %v", err)
	}
}

func TestRunTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	env := map[string]string{"RELAYDECK_URL": srv.URL, "RELAYDECK_AGENT_TOKEN": "tok"}
	start := time.Now()
	err := Run("Stop", strings.NewReader("{}"), func(k string) string { return env[k] }, nil)
	if err == nil {
		t.Error("want timeout error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestRunStdinNeverClosedIsCutOff(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	env := map[string]string{"RELAYDECK_URL": "http://127.0.0.1:1", "RELAYDECK_AGENT_TOKEN": "tok"}
	start := time.Now()
	err := Run("Stop", pr, func(k string) string { return env[k] }, nil)
	if err == nil {
		t.Error("want error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestRunDoesNotFollowRedirects(t *testing.T) {
	var evilHits int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&evilHits, 1) }))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	env := map[string]string{"RELAYDECK_URL": srv.URL, "RELAYDECK_AGENT_TOKEN": "tok"}
	err := Run("Stop", strings.NewReader("{}"), func(k string) string { return env[k] }, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Errorf("err = %v", err)
	}
	if atomic.LoadInt32(&evilHits) != 0 {
		t.Error("redirect was followed")
	}
}

func TestRunRejectsNonLoopbackURLAndEmptyToken(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	for _, u := range []string{"http://example.com:7341", "http://10.0.0.1:7341", "http://127.0.0.1.evil.test", "ftp://127.0.0.1", "not a url"} {
		env := map[string]string{"RELAYDECK_URL": u, "RELAYDECK_AGENT_TOKEN": "tok"}
		if err := Run("Stop", strings.NewReader("{}"), func(k string) string { return env[k] }, nil); err == nil {
			t.Errorf("url %q accepted", u)
		}
	}
	env := map[string]string{"RELAYDECK_URL": srv.URL}
	if err := Run("Stop", strings.NewReader("{}"), func(k string) string { return env[k] }, srv.Client()); err == nil {
		t.Error("empty token accepted")
	}
	if hits != 0 {
		t.Error("request sent")
	}
	env = map[string]string{"RELAYDECK_URL": "http://localhost:1", "RELAYDECK_AGENT_TOKEN": "tok"}
	err := Run("Stop", strings.NewReader("{}"), func(k string) string { return env[k] }, nil)
	if err != nil && strings.Contains(err.Error(), "not allowed") {
		t.Errorf("localhost refused: %v", err)
	}
}

func TestHandlerCapsFields(t *testing.T) {
	var got Report
	h := guarded(Handler(func(_ string, r Report) { got = r }))
	msg := strings.Repeat("é", 2000) // 4000 bytes
	body := `{"event":"Notification","message":"` + msg + `","source":"` + strings.Repeat("a", 65) + `","notification_type":"bad\u0000type"}`
	if rec := post(h, body); rec.Code != 204 {
		t.Fatalf("code = %d", rec.Code)
	}
	if len(got.Message) > 2048 || !utf8.ValidString(got.Message) || got.Message == "" {
		t.Errorf("message len %d valid %v", len(got.Message), utf8.ValidString(got.Message))
	}
	if got.Source != "" || got.NotificationType != "" {
		t.Errorf("source %q type %q kept", got.Source, got.NotificationType)
	}
	if rec := post(h, `{"event":"Notification","source":"startup","notification_type":"idle_prompt"}`); rec.Code != 204 || got.Source != "startup" || got.NotificationType != "idle_prompt" {
		t.Errorf("good values dropped: %+v", got)
	}
}

func TestStatusFor(t *testing.T) {
	tests := []struct {
		r      Report
		status string
		ok     bool
	}{
		{Report{Event: "SessionStart"}, "idle", true},
		{Report{Event: "UserPromptSubmit"}, "busy", true},
		{Report{Event: "Stop"}, "idle", true},
		{Report{Event: "Notification", NotificationType: "idle_prompt"}, "idle", true},
		{Report{Event: "Notification", NotificationType: "permission_prompt"}, "attention", true},
		{Report{Event: "Notification", NotificationType: "whatever"}, "attention", true},
		{Report{Event: "Notification"}, "attention", true},
		{Report{Event: "SessionEnd"}, "stopped", true},
		{Report{Event: "Nope"}, "", false},
	}
	for _, tt := range tests {
		s, ok := StatusFor(tt.r)
		if s != tt.status || ok != tt.ok {
			t.Errorf("StatusFor(%+v) = %q, %v", tt.r, s, ok)
		}
	}
}

func TestHandlerPassesSessionID(t *testing.T) {
	var gotID string
	var gotR Report
	h := guarded(Handler(func(id string, r Report) { gotID, gotR = id, r }))
	rec := post(h, `{"event":"SessionStart","session_id":"`+goodID+`","source":"clear","agent_id":"evil"}`)
	if rec.Code != 204 {
		t.Fatalf("code = %d", rec.Code)
	}
	if gotID != "a1" || gotR.SessionID != goodID || gotR.Source != "clear" || gotR.Event != "SessionStart" {
		t.Errorf("got %q %+v", gotID, gotR)
	}
}

func TestHandlerSessionIDMustBeLowercaseUUID(t *testing.T) {
	for _, bad := range []string{"../../x", strings.ToUpper(goodID), "--flag", goodID + "x", ""} {
		var got Report
		h := guarded(Handler(func(_ string, r Report) { got = r }))
		if rec := post(h, `{"event":"Stop","session_id":"`+bad+`"}`); rec.Code != 204 {
			t.Fatalf("code = %d", rec.Code)
		}
		if got.SessionID != "" {
			t.Errorf("session id %q kept", bad)
		}
	}
}

func TestHandlerRejects(t *testing.T) {
	called := false
	inner := Handler(func(string, Report) { called = true })
	h := guarded(inner)

	req := httptest.NewRequest("GET", "http://127.0.0.1:7341/agent/hook", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("GET = %d", rec.Code)
	}
	for body, want := range map[string]int{
		`{"event":"Bogus"}`: 400,
		`not json`:          400,
		`{"event":"Stop","message":"` + strings.Repeat("a", 70000) + `"}`: 413,
	} {
		if rec := post(h, body); rec.Code != want {
			t.Errorf("%.30s = %d, want %d", body, rec.Code, want)
		}
	}
	// No agent id in context (handler used without the guard).
	req = httptest.NewRequest("POST", "/agent/hook", strings.NewReader(`{"event":"Stop"}`))
	rec = httptest.NewRecorder()
	inner.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("no agent = %d", rec.Code)
	}
	if called {
		t.Error("onEvent called for a rejected request")
	}
}
