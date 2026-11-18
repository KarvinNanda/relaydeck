package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/daemon"
	"github.com/KarvinNanda/relaydeck/internal/logx"
	"github.com/coder/websocket"
)

const (
	// queryReplyWindow: for this long after the replay, input frames that are
	// only terminal query replies are dropped (ruling 10).
	queryReplyWindow = time.Second
	// The nudge must fit on one line of the input box (Task 11), so the
	// terminal is never smaller than this.
	minCols, minRows = 60, 10
	maxCols, maxRows = 1000, 1000
	wsWriteTimeout   = 10 * time.Second
	// How long to wait for Done after the output channel closed, to tell
	// "agent stopped" from "this client was too slow and was dropped".
	stopSettle = 500 * time.Millisecond
)

// queryReplyRe matches a frame made only of terminal query replies. When the
// replay is written into xterm.js, it answers the queries inside the old
// output; those answers must not reach the agent as typed input. Replies:
//   - CSI: cursor position (R), device status (n), DA1 and DA2 (c, with an
//     optional ? or > prefix);
//   - DECRQM mode reports: ESC [ ? or ESC [, digits, $y;
//   - DCS replies: ESC P ... ESC \ (DECRQSS, XTGETTCAP);
//   - OSC 10, 11 and 12 color reports, ended by BEL or ESC \.
//
// Shift+F3 and Ctrl+F3 send ESC [ 1 ; 2 R (or 5 R), which looks like a cursor
// position reply. They are dropped only inside the 1 s window after a replay
// (queryReplyWindow), never later.
//
// The OSC and DCS bodies cannot contain ESC or BEL, so a reply never swallows
// typed text that follows it.
var queryReplyRe = regexp.MustCompile(
	`^(?:\x1b\[[?>]?[0-9;]*[Rcn]` +
		`|\x1b\[\??[0-9;]*\$y` +
		`|\x1bP[^\x1b\x07]*\x1b\\` +
		`|\x1b\]1[0-2];[^\x1b\x07]*(?:\x07|\x1b\\)` +
		`)+$`)

func (a *api) wsRoutes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /ws/term/{ws}/{name}", a.term)
	m.HandleFunc("GET /ws/events", a.events)
	m.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such route")
	})
	return m
}

// accept upgrades the request. Guard.UIWebSocket has already checked Host,
// Origin and the ticket; the library checks the Origin again against the
// same two hosts.
func (a *api) accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	p := strconv.Itoa(a.g.Port)
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"127.0.0.1:" + p, "localhost:" + p},
	})
	if err != nil {
		a.log.Warn("websocket upgrade failed", "path", r.URL.Path, "request_id", logx.RequestID(r.Context()), "error", err.Error())
		return nil, err
	}
	c.SetReadLimit(maxBody)
	return c, nil
}

func write(ctx context.Context, c *websocket.Conn, typ websocket.MessageType, p []byte) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return c.Write(ctx, typ, p)
}

// term streams one agent's terminal. The server sends the scrollback replay
// as one binary frame, then live output as binary frames. The client sends
// JSON text frames: {"type":"input","data":string} or
// {"type":"resize","cols":int,"rows":int}.
//
// A slow client never blocks the agent: the agent drops a subscriber whose
// buffer is full, which closes the channel, and then this socket is closed
// with "try again later" so the UI reconnects and gets a fresh replay.
func (a *api) term(w http.ResponseWriter, r *http.Request) {
	ws, name := r.PathValue("ws"), r.PathValue("name")
	ag, ok := a.d.Agent(ws, name)
	if !ok {
		writeError(w, http.StatusNotFound, "folder "+ws+"/"+name+" has no agent; start it first")
		return
	}
	c, err := a.accept(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	log := a.log.With("request_id", logx.RequestID(r.Context()), "agent_id", ag.ID(), "ws", ws, "folder", name)
	log.Info("terminal attached")
	defer log.Info("terminal detached")

	replay, out, cancel := ag.Subscribe()
	defer cancel()
	// Not the request context: after the upgrade it does not end when the
	// client goes away. The input loop cancels this one instead.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := write(ctx, c, websocket.MessageBinary, replay); err != nil {
		return
	}
	replayAt := time.Now()
	go func() {
		defer stop()
		termInput(ctx, c, ag, replayAt, log)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-out:
			if !ok {
				select {
				case <-ag.Done():
					c.Close(websocket.StatusNormalClosure, "agent stopped")
				case <-time.After(stopSettle):
					c.Close(websocket.StatusTryAgainLater, "terminal fell behind; reconnect")
				}
				return
			}
			if err := write(ctx, c, websocket.MessageBinary, p); err != nil {
				return
			}
		}
	}
}

type termFrame struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// termInput applies client frames until the socket ends. It never logs what
// was typed.
func termInput(ctx context.Context, c *websocket.Conn, ag *agent.Agent, replayAt time.Time, log *slog.Logger) {
	for {
		typ, p, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var f termFrame
		if err := json.Unmarshal(p, &f); err != nil {
			log.Warn("terminal frame ignored", "reason", "not a JSON object")
			continue
		}
		switch f.Type {
		case "input":
			if time.Since(replayAt) < queryReplyWindow && queryReplyRe.MatchString(f.Data) {
				continue
			}
			if err := ag.Write([]byte(f.Data)); err != nil && !errors.Is(err, agent.ErrStopped) {
				log.Warn("terminal input failed", "error", err.Error())
			}
		case "resize":
			cols, rows := clamp(f.Cols, minCols, maxCols), clamp(f.Rows, minRows, maxRows)
			if err := ag.Resize(uint16(cols), uint16(rows)); err != nil && !errors.Is(err, agent.ErrStopped) {
				log.Warn("terminal resize failed", "error", err.Error())
			}
		default:
			log.Warn("terminal frame ignored", "reason", "unknown type")
		}
	}
}

func clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

// stateEvent is the first frame on /ws/events: the full State with
// "type":"state" next to its fields (ruling 11).
type stateEvent struct {
	Type string `json:"type"`
	daemon.State
}

// events streams daemon events as JSON text frames, after a full state
// snapshot. When the stream ends (shutdown, or this client fell behind and
// missed events), the socket is closed with "going away" and the UI
// reconnects for a new snapshot. Frames from the client are not expected.
func (a *api) events(w http.ResponseWriter, r *http.Request) {
	c, err := a.accept(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	log := a.log.With("request_id", logx.RequestID(r.Context()))
	log.Info("events attached")
	defer log.Info("events detached")

	// Subscribe before the snapshot, so no change falls between the two.
	evs, cancel := a.d.Subscribe()
	defer cancel()
	ctx := c.CloseRead(context.Background())
	if err := writeJSONFrame(ctx, c, stateEvent{Type: "state", State: a.d.State()}); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-evs:
			if !ok {
				c.Close(websocket.StatusGoingAway, "event stream ended; reconnect for a new snapshot")
				return
			}
			if err := writeJSONFrame(ctx, c, ev); err != nil {
				return
			}
		}
	}
}

func writeJSONFrame(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return write(ctx, c, websocket.MessageText, b)
}
