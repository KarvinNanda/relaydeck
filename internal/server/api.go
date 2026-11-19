package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/attach"
	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/daemon"
	"github.com/KarvinNanda/relaydeck/internal/launch"
	"github.com/KarvinNanda/relaydeck/internal/logx"
	"github.com/KarvinNanda/relaydeck/internal/notify"
	"github.com/KarvinNanda/relaydeck/internal/sessions"
)

// maxBody caps every request body and every WebSocket frame from the UI.
const maxBody = 64 << 10

// api holds the UI routes. Every route here sits behind Guard.UI (the
// /api/ routes) or Guard.UIWebSocket (the /ws/ routes); see New.
type api struct {
	d      *daemon.Daemon
	g      *auth.Guard
	picker FolderPicker
	log    *slog.Logger

	// pickMu is held while a folder dialog is open: one dialog at a time.
	pickMu sync.Mutex

	// seats decides which terminal socket of an agent may send query replies.
	seats termSeats
}

func (a *api) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/state", a.state)
	m.HandleFunc("POST /api/workspaces", a.addWorkspace)
	m.HandleFunc("POST /api/workspaces/{ws}/folders", a.addFolder)
	m.HandleFunc("POST /api/pick-folder", a.pickFolder)
	m.HandleFunc("GET /api/workspaces/{ws}/folders/{name}/sessions", a.sessions)
	m.HandleFunc("POST /api/agents/{ws}/{name}/start", a.start)
	m.HandleFunc("POST /api/agents/{ws}/{name}/stop", a.stop)
	m.HandleFunc("POST /api/agents/{ws}/{name}/deliver-now", a.deliverNow)
	m.HandleFunc("POST /api/agents/{ws}/{name}/attachments", a.attachment)
	m.HandleFunc("GET /api/workspaces/{ws}/messages", a.messages)
	m.HandleFunc("POST /api/messages/{id}/approve", a.approve)
	m.HandleFunc("POST /api/messages/{id}/reject", a.reject)
	m.HandleFunc("POST /api/ws-ticket", a.wsTicket)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such route")
	})
	return m
}

func (a *api) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.d.State())
}

func (a *api) addWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !readBody(w, r, &in, false) {
		return
	}
	if err := a.d.AddWorkspace(in.Name); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": in.Name})
}

// folderJSON is a folder as the API returns it.
type folderJSON struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Account        string `json:"account,omitempty"`
	PermissionMode string `json:"permission_mode"`
}

func (a *api) addFolder(w http.ResponseWriter, r *http.Request) {
	var in folderJSON
	if !readBody(w, r, &in, false) {
		return
	}
	// A relative path would be read against the daemon's working directory,
	// which means nothing to the user. "~/x" is expanded by Canonicalize.
	if in.Path != "~" && !strings.HasPrefix(in.Path, "~/") && !strings.HasPrefix(in.Path, `~\`) && !filepath.IsAbs(in.Path) {
		writeError(w, http.StatusBadRequest, "path must be an absolute path to an existing directory")
		return
	}
	f, err := a.d.AddFolder(r.PathValue("ws"), config.Folder{
		Name:           in.Name,
		Path:           in.Path,
		Account:        in.Account,
		PermissionMode: in.PermissionMode,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := folderJSON{Name: f.Name, Path: f.Path, Account: f.Account, PermissionMode: f.PermissionMode}
	if out.PermissionMode == "" {
		out.PermissionMode = "auto"
	}
	writeJSON(w, http.StatusCreated, out)
}

// pickFolder opens the native folder dialog on this machine and waits for
// the user. A second request while a dialog is open gets 409 instead of
// waiting behind it. The dialog error is logged, not returned: it can hold
// details of the desktop session that the browser does not need.
func (a *api) pickFolder(w http.ResponseWriter, r *http.Request) {
	if !a.pickMu.TryLock() {
		writeError(w, http.StatusConflict, "a folder dialog is already open; finish or close it first")
		return
	}
	defer a.pickMu.Unlock()
	p, err := a.picker.PickFolder(r.Context())
	switch {
	case errors.Is(err, ErrPickCancelled):
		writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
	case err != nil:
		a.log.Error("folder dialog failed", "request_id", logx.RequestID(r.Context()), "error", err.Error())
		writeError(w, http.StatusInternalServerError, "could not open the folder dialog; type the path instead")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"path": p})
	}
}

type sessionJSON struct {
	ID       string `json:"id"`
	Modified string `json:"modified"` // RFC 3339
	Title    string `json:"title"`
}

func (a *api) sessions(w http.ResponseWriter, r *http.Request) {
	list, err := a.d.Sessions(r.PathValue("ws"), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]sessionJSON{"sessions": toSessionJSON(list)})
}

func toSessionJSON(list []sessions.Session) []sessionJSON {
	out := make([]sessionJSON, len(list))
	for i, s := range list {
		out[i] = sessionJSON{ID: s.ID, Modified: s.Modified.UTC().Format(time.RFC3339), Title: s.Title}
	}
	return out
}

func (a *api) start(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID string `json:"session_id"`
		New       bool   `json:"new"`
	}
	if !readBody(w, r, &in, true) {
		return
	}
	if err := a.d.StartAgent(r.PathValue("ws"), r.PathValue("name"), in.SessionID, in.New); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stop refuses an agent with a draft (409, the daemon's text) unless
// ?force=true.
func (a *api) stop(w http.ResponseWriter, r *http.Request) {
	force := false
	if v := r.URL.Query().Get("force"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "force must be true or false")
			return
		}
		force = b
	}
	if err := a.d.StopAgent(r.PathValue("ws"), r.PathValue("name"), force); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) deliverNow(w http.ResponseWriter, r *http.Request) {
	if err := a.d.DeliverNow(r.PathValue("ws"), r.PathValue("name")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// attachment saves a dropped or pasted image for the running agent and answers
// its path; the panel then pastes the path into the terminal, and Claude Code
// turns it into an [Image #N]. The body is the raw image, at most
// attach.MaxFile bytes. The type is sniffed by attach.Save.
func (a *api) attachment(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("ws"), r.PathValue("name")
	dir, err := a.d.AttachmentDir(ws, name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// MaxBytesReader is a backstop only: attach.Save stops reading at
	// MaxFile+1 bytes, so it reports ErrTooLarge before this limit is hit.
	p, err := attach.Save(dir, http.MaxBytesReader(w, r.Body, attach.MaxFile+1), attach.MaxFile, attach.MaxTotal)
	switch {
	case err == nil:
	case errors.Is(err, attach.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, attach.ErrTooLarge.Error())
		return
	case errors.Is(err, attach.ErrNotImage):
		writeError(w, http.StatusUnsupportedMediaType, attach.ErrNotImage.Error())
		return
	case errors.Is(err, attach.ErrFull):
		writeError(w, http.StatusConflict, attach.ErrFull.Error())
		return
	case errors.Is(err, fs.ErrNotExist):
		// The agent exited between the lookup and the save: its runtime dir
		// is gone, and attach.Save does not create it again.
		writeError(w, http.StatusConflict, daemon.ErrNotRunning.Error())
		return
	default:
		a.fail(w, r, err)
		return
	}
	// The agent may have exited while the body was read: then its runtime
	// dir is being removed and the path would be pasted into a dead
	// terminal. Drop the file and say so (#72).
	if now, err := a.d.AttachmentDir(ws, name); err != nil || now != dir {
		os.Remove(p)
		writeError(w, http.StatusConflict, daemon.ErrNotRunning.Error())
		return
	}
	fi, _ := os.Stat(p)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	a.log.Info("attachment saved", "request_id", logx.RequestID(r.Context()),
		"agent_id", filepath.Base(filepath.Dir(dir)), "ws", ws, "folder", name, "ext", filepath.Ext(p), "bytes", size)
	writeJSON(w, http.StatusOK, map[string]string{"path": p})
}

func (a *api) messages(w http.ResponseWriter, r *http.Request) {
	msgs, err := a.d.Messages(r.PathValue("ws"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]notify.Message{"messages": msgs})
}

func (a *api) approve(w http.ResponseWriter, r *http.Request) {
	a.noContent(w, r, a.d.Approve(r.PathValue("id")))
}

func (a *api) reject(w http.ResponseWriter, r *http.Request) {
	a.noContent(w, r, a.d.Reject(r.PathValue("id")))
}

func (a *api) noContent(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// wsTicket trades the UI token for a single-use WebSocket ticket (browsers
// cannot set headers on a WebSocket upgrade).
func (a *api) wsTicket(w http.ResponseWriter, r *http.Request) {
	t, err := a.g.Tickets.Issue()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": t})
}

// statusOf maps daemon errors to HTTP codes.
func statusOf(err error) int {
	switch {
	case errors.Is(err, daemon.ErrNotFound), errors.Is(err, notify.ErrUnknownMessage):
		return http.StatusNotFound
	case errors.Is(err, daemon.ErrInvalid), errors.Is(err, daemon.ErrNoTranscript),
		errors.Is(err, launch.ErrBadInput):
		return http.StatusBadRequest
	case errors.Is(err, daemon.ErrConflict), errors.Is(err, daemon.ErrRunning),
		errors.Is(err, daemon.ErrNotRunning), errors.Is(err, daemon.ErrDraft),
		errors.Is(err, daemon.ErrExternalOpen),
		errors.Is(err, notify.ErrNotAwaitingApproval), errors.Is(err, launch.ErrCswapMissing):
		return http.StatusConflict
	// The request is fine but this server cannot do the work right now.
	case errors.Is(err, daemon.ErrShuttingDown), errors.Is(err, daemon.ErrClaudeNotFound),
		errors.Is(err, daemon.ErrExternalCheck):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// fail writes the error. A 500 gets a fixed text and a 503 gets the fixed
// text of its sentinel; the full error goes to the log with the request_id.
// A 503 never sends err.Error(): it can hold subprocess output or absolute
// paths. No error text here carries a token.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	code := statusOf(err)
	msg := err.Error()
	switch code {
	case http.StatusInternalServerError:
		a.log.Error("api error", "path", r.URL.Path, "request_id", logx.RequestID(r.Context()), "error", msg)
		msg = "internal error; see the relaydeck log"
	case http.StatusServiceUnavailable:
		a.log.Error("api unavailable", "path", r.URL.Path, "request_id", logx.RequestID(r.Context()), "error", msg)
		switch {
		case errors.Is(err, daemon.ErrShuttingDown):
			msg = daemon.ErrShuttingDown.Error()
		case errors.Is(err, daemon.ErrClaudeNotFound):
			msg = daemon.ErrClaudeNotFound.Error()
		case errors.Is(err, daemon.ErrExternalCheck):
			msg = daemon.ErrExternalCheck.Error()
		default:
			msg = "service unavailable; see the relaydeck log"
		}
	}
	writeError(w, code, msg)
}

// readBody decodes a JSON object of at most maxBody bytes into v. Unknown
// fields and trailing data are refused. With optional, an empty body is
// fine. On false the 400 is already written.
func readBody(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusBadRequest, "request body over 64 KiB")
			return false
		}
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return false
	}
	if optional && len(bytes.TrimSpace(b)) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return false
	}
	// A second Decode must hit the end of the body. dec.More is not enough:
	// it says false for a stray closing bracket.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: extra data after the object")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
