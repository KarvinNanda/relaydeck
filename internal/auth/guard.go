package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
)

// AgentTokens maps an agent token to the agent it belongs to.
// Implementations should look tokens up by sha256(token) (or compare in constant time), never by a prefix scan.
type AgentTokens interface {
	Lookup(token string) (agentID string, ok bool)
}

// Guard is the security boundary for every HTTP and WebSocket route.
// Each middleware checks in this order: Host (421), Origin (403), credentials (401).
type Guard struct {
	Port    int
	UIToken string
	Agents  AgentTokens
	Tickets *TicketStore
}

type ctxKey struct{}

// AgentID returns the agent id that Guard.Agent put in the request context.
func AgentID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxKey{}).(string)
	return id, ok
}

func (g *Guard) hostOK(r *http.Request) bool {
	p := strconv.Itoa(g.Port)
	h := strings.ToLower(r.Host)
	return h == "127.0.0.1:"+p || h == "localhost:"+p
}

func (g *Guard) originOK(v string) bool {
	p := strconv.Itoa(g.Port)
	o := strings.ToLower(v)
	return o == "http://127.0.0.1:"+p || o == "http://localhost:"+p
}

// origins returns the Origin header values and whether the header is present.
func origins(r *http.Request) ([]string, bool) {
	v, ok := r.Header["Origin"]
	return v, ok
}

// checkHostOrigin writes the error response and returns false when the request
// must stop. requireOrigin makes a missing Origin an error.
func (g *Guard) checkHostOrigin(w http.ResponseWriter, r *http.Request, requireOrigin bool) bool {
	if !g.hostOK(r) {
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return false
	}
	vals, present := origins(r)
	if present {
		if len(vals) != 1 || !g.originOK(vals[0]) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return false
		}
	} else if requireOrigin {
		http.Error(w, "origin required", http.StatusForbidden)
		return false
	}
	return true
}

func bearer(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	t := h[len(prefix):]
	return t, t != ""
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// UI guards routes used by the browser UI: Host, Origin (optional), UI bearer token.
func (g *Guard) UI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.checkHostOrigin(w, r, false) {
			return
		}
		t, ok := bearer(r)
		if !ok || g.UIToken == "" ||
			subtle.ConstantTimeCompare([]byte(t), []byte(g.UIToken)) != 1 {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Static guards routes that serve public files (the UI bundle): Host and Origin
// (if present) are checked, no credentials. This keeps a DNS-rebound origin out.
func (g *Guard) Static(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.checkHostOrigin(w, r, false) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// UIWebSocket guards the WebSocket upgrade: Host, a valid Origin (required),
// and a single-use ticket in the "ticket" query parameter.
func (g *Guard) UIWebSocket(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.checkHostOrigin(w, r, true) {
			return
		}
		t := r.URL.Query().Get("ticket")
		if t == "" || g.Tickets == nil || !g.Tickets.Redeem(t) {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Agent guards routes used by hooks and the MCP subprocess. These are not
// browsers, so any Origin header is refused.
func (g *Guard) Agent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.hostOK(r) {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		if _, present := origins(r); present {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		t, ok := bearer(r)
		if !ok || g.Agents == nil {
			unauthorized(w)
			return
		}
		id, ok := g.Agents.Lookup(t)
		if !ok {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}
