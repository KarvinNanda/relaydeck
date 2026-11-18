package auth

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAgents map[string]string

func (f fakeAgents) Lookup(token string) (string, bool) {
	id, ok := f[token]
	return id, ok
}

const (
	uiTok = "ui-token-for-tests"
	port  = 7341
)

func newGuard() *Guard {
	return &Guard{
		Port:    port,
		UIToken: uiTok,
		Agents:  fakeAgents{"tok-a": "agent-a", "tok-b": "agent-b"},
		Tickets: NewTicketStore(time.Now),
	}
}

type req struct {
	host, origin, auth, method, query string
	setOrigin                         bool
}

func (c req) build() *http.Request {
	m := c.method
	if m == "" {
		m = http.MethodPost
	}
	r := httptest.NewRequest(m, "/x"+c.query, nil)
	r.Host = c.host
	if c.setOrigin {
		r.Header.Set("Origin", c.origin)
	}
	if c.auth != "" {
		r.Header.Set("Authorization", c.auth)
	}
	return r
}

func run(h http.Handler, r *http.Request) (int, string) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func ok200() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestGuardUI(t *testing.T) {
	bearer := "Bearer " + uiTok
	tests := []struct {
		name string
		c    req
		want int
	}{
		{"ip host", req{host: "127.0.0.1:7341", auth: bearer}, 200},
		{"localhost host", req{host: "localhost:7341", auth: bearer}, 200},
		{"uppercase host", req{host: "LOCALHOST:7341", auth: bearer}, 200},
		{"evil host", req{host: "evil.example:7341", auth: bearer}, 421},
		{"wrong port", req{host: "127.0.0.1:9999", auth: bearer}, 421},
		{"empty host", req{host: "", auth: bearer}, 421},
		{"ipv6 host", req{host: "[::1]:7341", auth: bearer}, 421},
		{"no port", req{host: "127.0.0.1", auth: bearer}, 421},
		{"trailing dot", req{host: "localhost.:7341", auth: bearer}, 421},
		{"evil origin", req{host: "127.0.0.1:7341", auth: bearer, origin: "http://evil.example", setOrigin: true}, 403},
		{"good origin", req{host: "127.0.0.1:7341", auth: bearer, origin: "http://localhost:7341", setOrigin: true}, 200},
		{"uppercase origin", req{host: "127.0.0.1:7341", auth: bearer, origin: "HTTP://LOCALHOST:7341", setOrigin: true}, 200},
		{"null origin", req{host: "127.0.0.1:7341", auth: bearer, origin: "null", setOrigin: true}, 403},
		{"empty origin value", req{host: "127.0.0.1:7341", auth: bearer, origin: "", setOrigin: true}, 403},
		{"https origin", req{host: "127.0.0.1:7341", auth: bearer, origin: "https://localhost:7341", setOrigin: true}, 403},
		{"origin wrong port", req{host: "127.0.0.1:7341", auth: bearer, origin: "http://localhost:9999", setOrigin: true}, 403},
		{"no origin valid token", req{host: "127.0.0.1:7341", auth: bearer}, 200},
		{"wrong token", req{host: "127.0.0.1:7341", auth: "Bearer nope"}, 401},
		{"missing token", req{host: "127.0.0.1:7341"}, 401},
		{"not bearer scheme", req{host: "127.0.0.1:7341", auth: "Basic " + uiTok}, 401},
		{"agent token on ui", req{host: "127.0.0.1:7341", auth: "Bearer tok-a"}, 401},
		{"host checked before token", req{host: "evil.example:7341", auth: "Bearer nope"}, 421},
		{"origin checked before token", req{host: "127.0.0.1:7341", auth: "Bearer nope", origin: "http://evil.example", setOrigin: true}, 403},
	}
	h := newGuard().UI(ok200())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := run(h, tc.c.build()); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGuardUIEmptyTokenRejectsEverything(t *testing.T) {
	g := newGuard()
	g.UIToken = ""
	h := g.UI(ok200())
	for _, a := range []string{"", "Bearer ", "Bearer"} {
		if got, _ := run(h, req{host: "127.0.0.1:7341", auth: a}.build()); got != 401 {
			t.Fatalf("auth %q: status = %d, want 401", a, got)
		}
	}
}

func TestGuardNeverEchoesTokens(t *testing.T) {
	g := newGuard()
	for _, h := range []http.Handler{g.UI(ok200()), g.Agent(ok200())} {
		_, body := run(h, req{host: "127.0.0.1:7341", auth: "Bearer received-secret-xyz"}.build())
		if strings.Contains(body, "received-secret-xyz") || strings.Contains(body, uiTok) {
			t.Fatalf("response body leaks a token: %q", body)
		}
	}
}

func TestGuardAgent(t *testing.T) {
	g := newGuard()
	var gotID string
	var gotOK bool
	h := g.Agent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, gotOK = AgentID(r.Context())
	}))

	tests := []struct {
		name string
		c    req
		want int
		id   string
	}{
		{"agent a", req{host: "127.0.0.1:7341", auth: "Bearer tok-a"}, 200, "agent-a"},
		{"agent b", req{host: "localhost:7341", auth: "Bearer tok-b"}, 200, "agent-b"},
		{"ui token", req{host: "127.0.0.1:7341", auth: "Bearer " + uiTok}, 401, ""},
		{"unknown token", req{host: "127.0.0.1:7341", auth: "Bearer zzz"}, 401, ""},
		{"missing token", req{host: "127.0.0.1:7341"}, 401, ""},
		{"bad host", req{host: "evil.example:7341", auth: "Bearer tok-a"}, 421, ""},
		{"any origin", req{host: "127.0.0.1:7341", auth: "Bearer tok-a", origin: "http://localhost:7341", setOrigin: true}, 403, ""},
		{"null origin", req{host: "127.0.0.1:7341", auth: "Bearer tok-a", origin: "null", setOrigin: true}, 403, ""},
		{"empty origin", req{host: "127.0.0.1:7341", auth: "Bearer tok-a", origin: "", setOrigin: true}, 403, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotOK = "", false
			if got, _ := run(h, tc.c.build()); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
			if tc.want == 200 && (!gotOK || gotID != tc.id) {
				t.Fatalf("AgentID = %q,%v want %q", gotID, gotOK, tc.id)
			}
			if tc.want != 200 && gotOK {
				t.Fatal("handler ran on a rejected request")
			}
		})
	}
}

func TestAgentIDAbsent(t *testing.T) {
	if _, ok := AgentID(context.Background()); ok {
		t.Fatal("AgentID ok on empty context")
	}
}

func TestGuardUIWebSocket(t *testing.T) {
	g := newGuard()
	h := g.UIWebSocket(ok200())
	good := func() req {
		return req{method: http.MethodGet, host: "127.0.0.1:7341", origin: "http://127.0.0.1:7341", setOrigin: true}
	}
	issue := func() string {
		tk, err := g.Tickets.Issue()
		if err != nil {
			t.Fatal(err)
		}
		return "?ticket=" + tk
	}

	c := good()
	c.query = issue()
	if got, _ := run(h, c.build()); got != 200 {
		t.Fatalf("valid ticket: %d", got)
	}
	// Same ticket again: single use.
	if got, _ := run(h, c.build()); got != 401 {
		t.Fatalf("reused ticket: %d, want 401", got)
	}

	c = good()
	c.setOrigin = false
	c.query = issue()
	if got, _ := run(h, c.build()); got != 403 {
		t.Fatalf("missing origin: %d, want 403", got)
	}

	c = good()
	c.origin = "http://evil.example"
	c.query = issue()
	if got, _ := run(h, c.build()); got != 403 {
		t.Fatalf("bad origin: %d, want 403", got)
	}

	c = good()
	c.host = "evil.example:7341"
	c.query = issue()
	if got, _ := run(h, c.build()); got != 421 {
		t.Fatalf("bad host: %d, want 421", got)
	}

	c = good()
	if got, _ := run(h, c.build()); got != 401 {
		t.Fatalf("no ticket: %d, want 401", got)
	}
	c.query = "?ticket=forged"
	if got, _ := run(h, c.build()); got != 401 {
		t.Fatalf("forged ticket: %d, want 401", got)
	}
	// The UI bearer token is not a substitute for a ticket.
	c = good()
	c.auth = "Bearer " + uiTok
	if got, _ := run(h, c.build()); got != 401 {
		t.Fatalf("bearer instead of ticket: %d, want 401", got)
	}
}

func TestTicket(t *testing.T) {
	now := time.Unix(1000, 0)
	s := NewTicketStore(func() time.Time { return now })

	tk, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Redeem(tk) {
		t.Fatal("first redeem failed")
	}
	if s.Redeem(tk) {
		t.Fatal("second redeem succeeded")
	}

	tk, _ = s.Issue()
	now = now.Add(31 * time.Second)
	if s.Redeem(tk) {
		t.Fatal("redeem after 31s succeeded")
	}
	if s.Redeem(tk) {
		t.Fatal("expired ticket redeemed on retry")
	}

	tk, _ = s.Issue()
	now = now.Add(29 * time.Second)
	if !s.Redeem(tk) {
		t.Fatal("redeem at 29s failed")
	}
	if s.Redeem("") || s.Redeem("unknown") {
		t.Fatal("unknown ticket redeemed")
	}
}

func TestTicketPrunesExpiredOnIssue(t *testing.T) {
	now := time.Unix(1000, 0)
	s := NewTicketStore(func() time.Time { return now })
	for i := 0; i < 50; i++ {
		if _, err := s.Issue(); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Minute)
	if _, err := s.Issue(); err != nil {
		t.Fatal(err)
	}
	if n := s.size(); n != 1 {
		t.Fatalf("store size = %d, want 1 after prune", n)
	}
}

func TestTicketConcurrentSingleUse(t *testing.T) {
	s := NewTicketStore(time.Now)
	tk, _ := s.Issue()
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Redeem(tk) {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("redeem won %d times, want 1", wins)
	}
}

func TestNewTokenLengthAndUniqueness(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		tk, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		b, err := base64.RawURLEncoding.DecodeString(tk)
		if err != nil || len(b) != 32 {
			t.Fatalf("token %q: decoded len %d, err %v", tk, len(b), err)
		}
		if seen[tk] {
			t.Fatalf("duplicate token %q", tk)
		}
		seen[tk] = true
	}
}

func TestGuardStatic(t *testing.T) {
	get := func(host, origin string, setOrigin bool) req {
		return req{method: http.MethodGet, host: host, origin: origin, setOrigin: setOrigin}
	}
	tests := []struct {
		name string
		c    req
		want int
	}{
		{"ip host no token", get("127.0.0.1:7341", "", false), 200},
		{"localhost host no token", get("localhost:7341", "", false), 200},
		{"good origin", get("127.0.0.1:7341", "http://localhost:7341", true), 200},
		{"evil host", get("evil.example:7341", "", false), 421},
		{"wrong port", get("127.0.0.1:9999", "", false), 421},
		{"empty host", get("", "", false), 421},
		{"evil origin", get("127.0.0.1:7341", "http://evil.example", true), 403},
		{"null origin", get("127.0.0.1:7341", "null", true), 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			h := newGuard().Static(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ran = true
			}))
			if got, _ := run(h, tc.c.build()); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
			if ran != (tc.want == 200) {
				t.Fatalf("handler ran = %v, want %v", ran, tc.want == 200)
			}
		})
	}
}
