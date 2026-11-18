// Package server is the HTTP layer of the daemon: the agent endpoints, the
// UI API and WebSockets, and the UI bundle.
package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/daemon"
	"github.com/KarvinNanda/relaydeck/internal/logx"
)

// New returns the root handler. Routes:
//   - /agent/hook and /agent/tools/{name}: behind Guard.Agent (agent token)
//   - every other /agent/ path: 401 without a token, 404 with one
//   - /api/...: the UI API (api.go), behind Guard.UI (UI token)
//   - /ws/term/{ws}/{name} and /ws/events (ws.go): behind Guard.UIWebSocket
//     (single-use ticket from POST /api/ws-ticket)
//   - /: the UI bundle from static, behind Guard.Static (no token); a
//     directory without index.html is 404
//
// Every request gets a request_id and one log line (logx.Middleware), and
// every response gets the security headers (securityHeaders).
func New(d *daemon.Daemon, g *auth.Guard, static fs.FS, picker FolderPicker, log *slog.Logger) http.Handler {
	ui := &api{d: d, g: g, picker: picker, log: log}
	agentMux := http.NewServeMux()
	agentMux.Handle("/agent/hook", d.HookHandler())
	agentMux.Handle("/agent/tools/", d.ToolsHandler())

	mux := http.NewServeMux()
	mux.Handle("/agent/", g.Agent(agentLog(log, d, agentMux)))
	mux.Handle("/api/", g.UI(ui.routes()))
	mux.Handle("/ws/", g.UIWebSocket(ui.wsRoutes()))
	mux.Handle("/", g.Static(staticFiles(static)))
	return logx.Middleware(log, securityHeaders(mux))
}

// contentSecurityPolicy allows only same-origin scripts, styles, fonts and
// connections. Styles also allow 'unsafe-inline' because xterm.js injects a
// <style> element; scripts never allow inline code or eval.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"font-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// securityHeaders sets the browser hardening headers on every response,
// before any handler (or guard) can write one.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

// staticFiles serves the UI bundle. A directory is served only through its
// index.html; without one it is 404, never a file listing.
func staticFiles(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		if st, err := fs.Stat(fsys, name); err == nil && st.IsDir() {
			if _, err := fs.Stat(fsys, path.Join(name, "index.html")); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		// FileServer takes the type from the OS (the registry on Windows),
		// which can be wrong for scripts and styles. A module script with the
		// wrong type is blocked by the browser.
		switch path.Ext(name) {
		case ".js", ".mjs":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		files.ServeHTTP(w, r)
	})
}

// agentLog adds one line per agent call with the agent's identity, so the
// request_id line from logx can be tied to an agent and folder. It never logs
// the body (hook messages, notify bodies) or the token.
func agentLog(log *slog.Logger, d *daemon.Daemon, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.AgentID(r.Context())
		ws, folder, _ := d.WhoAmI(id)
		log.Info("agent call",
			"path", r.URL.Path,
			"agent_id", id,
			"ws", ws,
			"folder", folder,
			"request_id", logx.RequestID(r.Context()),
		)
		next.ServeHTTP(w, r)
	})
}

// Listen binds 127.0.0.1:port. A port in use gives the message from Review
// Focus 5, naming the port and how to change it.
func Listen(port int, configPath string) (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		return ln, nil
	}
	if isAddrInUse(err) {
		return nil, fmt.Errorf("port %d is already in use. Another relaydeck may be running. Change \"port\" in %s or pass --port.", port, configPath)
	}
	return nil, fmt.Errorf("listen on 127.0.0.1:%d: %w", port, err)
}

func isAddrInUse(err error) bool { return errors.Is(err, addrInUseErrno) }
