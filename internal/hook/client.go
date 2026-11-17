// Package hook turns Claude Code hook events into agent status reports.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Events lists the hook events relaydeck registers.
var Events = []string{"SessionStart", "UserPromptSubmit", "Stop", "Notification", "SessionEnd"}

const (
	maxStdin   = 1 << 20
	maxMessage = 2048
	timeout    = 2 * time.Second
)

// Report is the small set of fields forwarded to the daemon.
// The prompt and the transcript path are never part of it.
type Report struct {
	Event            string `json:"event"`
	SessionID        string `json:"session_id"`
	Source           string `json:"source"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
}

func knownEvent(e string) bool {
	for _, k := range Events {
		if k == e {
			return true
		}
	}
	return false
}

// Run reads one hook payload from stdin and reports it to the daemon.
// Without RELAYDECK_URL it does nothing: the hooks may be present while relaydeck is not.
// A nil hc means a default client. The request is cut off after 2 seconds either way.
func Run(event string, stdin io.Reader, getenv func(string) string, hc *http.Client) error {
	if !knownEvent(event) {
		return fmt.Errorf("unknown hook event %q", event)
	}
	base := getenv("RELAYDECK_URL")
	if base == "" {
		return nil
	}
	var in struct {
		SessionID        string `json:"session_id"`
		Source           string `json:"source"`
		Message          string `json:"message"`
		NotificationType string `json:"notification_type"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, maxStdin)).Decode(&in); err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	if len(in.Message) > maxMessage {
		in.Message = in.Message[:maxMessage]
	}
	body, err := json.Marshal(Report{Event: event, SessionID: in.SessionID, Source: in.Source, Message: in.Message, NotificationType: in.NotificationType})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/agent/hook", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+getenv("RELAYDECK_AGENT_TOKEN"))
	if hc == nil {
		hc = &http.Client{}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// url.Error repeats the URL only, never headers, so the token cannot appear here.
		return fmt.Errorf("report hook: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("report hook: daemon answered %d", resp.StatusCode)
	}
	return nil
}
