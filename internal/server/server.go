// Package server is the HTTP layer of the daemon. 13a serves the agent
// endpoints and the UI bundle; the UI API and WebSockets come in 13b.
package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/daemon"
	"github.com/KarvinNanda/relaydeck/internal/logx"
)

// New returns the root handler. Routes:
//   - /agent/hook and /agent/tools/{name}: behind Guard.Agent (agent token)
//   - every other /agent/ path: 401 without a token, 404 with one
//   - /: the UI bundle from static, behind Guard.Static (no token)
//
// Every request gets a request_id and one log line (logx.Middleware).
func New(d *daemon.Daemon, g *auth.Guard, static fs.FS, log *slog.Logger) http.Handler {
	agentMux := http.NewServeMux()
	agentMux.Handle("/agent/hook", d.HookHandler())
	agentMux.Handle("/agent/tools/", d.ToolsHandler())

	mux := http.NewServeMux()
	mux.Handle("/agent/", g.Agent(agentLog(log, d, agentMux)))
	mux.Handle("/", g.Static(http.FileServerFS(static)))
	return logx.Middleware(log, mux)
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
