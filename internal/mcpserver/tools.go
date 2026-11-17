// Package mcpserver is the stdio MCP server each agent starts (Run) and the
// daemon side that answers its three tools (Handlers).
package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/notify"
)

const (
	maxBody     = 64 << 10
	readBudget  = 256 << 10 // bytes of envelopes per read_messages answer
	maxRespSize = 1 << 20   // the client refuses answers above this
)

type notifyArgs struct {
	Target  string `json:"target"`
	Message string `json:"message"`
}

// Handlers serves POST /agent/tools/{name}. The caller must be wrapped by
// auth.Guard.Agent: the identity comes only from the request context, never
// from the body. whoami maps the agent id to its workspace and folder name.
func Handlers(r *notify.Router, dir notify.Directory, status func(ws, folder string) string,
	whoami func(agentID string) (ws, folder string, ok bool)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agent/tools/{name}", func(w http.ResponseWriter, req *http.Request) {
		id, ok := auth.AgentID(req.Context())
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ws, folder, ok := whoami(id)
		if !ok {
			writeErr(w, http.StatusForbidden, "unknown agent")
			return
		}
		req.Body = http.MaxBytesReader(w, req.Body, maxBody)
		var text string
		var err error
		switch name := req.PathValue("name"); name {
		case "list_folders":
			text = listFolders(dir, status, ws, folder)
		case "notify":
			var a notifyArgs
			if derr := json.NewDecoder(req.Body).Decode(&a); derr != nil {
				var mbe *http.MaxBytesError
				if errors.As(derr, &mbe) {
					writeErr(w, http.StatusBadRequest, "request body over 64 KiB")
					return
				}
				writeErr(w, http.StatusBadRequest, "invalid request body")
				return
			}
			text, err = sendNotify(r, ws, folder, a)
		case "read_messages":
			text = readMessages(r, ws, folder)
		default:
			writeErr(w, http.StatusNotFound, fmt.Sprintf("unknown tool %q", name))
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
	})
	return mux
}

func listFolders(dir notify.Directory, status func(ws, folder string) string, ws, self string) string {
	names := append([]string(nil), dir.Folders(ws)...)
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, n := range names {
		l := fmt.Sprintf("%s (%s)", n, status(ws, n))
		if n == self {
			l += " (you)"
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

func sendNotify(r *notify.Router, ws, from string, a notifyArgs) (string, error) {
	m, err := r.Send(ws, from, a.Target, a.Message)
	if err != nil {
		return "", err
	}
	if m.State == notify.StateAwaitingApproval {
		return fmt.Sprintf("Message %s for %q is held: hop limit reached, waiting for the user to approve.", m.ID, m.To), nil
	}
	return fmt.Sprintf("Message %s queued for %q (hop %d).", m.ID, m.To, m.Hop), nil
}

func readMessages(r *notify.Router, ws, agent string) string {
	msgs, remaining := r.ReadN(ws, agent, readBudget)
	if len(msgs) == 0 {
		return "No messages."
	}
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = notify.Envelope(m)
	}
	out := strings.Join(parts, "\n\n")
	if remaining > 0 {
		out += fmt.Sprintf("\n\n[relaydeck] %d more messages waiting. Call read_messages again.", remaining)
	}
	return out
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
