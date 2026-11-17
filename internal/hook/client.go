// Package hook turns Claude Code hook events into agent status reports.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// loopbackURL accepts only an http(s) URL whose host is 127.0.0.1 or localhost,
// so the agent token can never be sent to another machine.
func loopbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("RELAYDECK_URL is not a valid http URL")
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" {
		return errors.New("RELAYDECK_URL host not allowed: only 127.0.0.1 and localhost")
	}
	return nil
}

// Run reads one hook payload from stdin and reports it to the daemon.
// Without RELAYDECK_URL it does nothing: the hooks may be present while relaydeck is not.
// A nil hc means a default client. One 2 second deadline covers the stdin read and the request.
// Redirects are never followed, so the bearer token only goes to RELAYDECK_URL.
func Run(event string, stdin io.Reader, getenv func(string) string, hc *http.Client) error {
	if !knownEvent(event) {
		return fmt.Errorf("unknown hook event %q", event)
	}
	base := getenv("RELAYDECK_URL")
	if base == "" {
		return nil
	}
	if err := loopbackURL(base); err != nil {
		return err
	}
	token := getenv("RELAYDECK_AGENT_TOKEN")
	if token == "" {
		return errors.New("RELAYDECK_AGENT_TOKEN is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type result struct {
		in  input
		err error
	}
	done := make(chan result, 1)
	go func() {
		var in input
		err := json.NewDecoder(io.LimitReader(stdin, maxStdin)).Decode(&in)
		done <- result{in, err}
	}()
	var in input
	select {
	case r := <-done:
		if r.err != nil {
			return fmt.Errorf("read hook input: %w", r.err)
		}
		in = r.in
	case <-ctx.Done():
		return errors.New("read hook input: timed out")
	}

	if len(in.Message) > maxMessage {
		in.Message = in.Message[:maxMessage]
	}
	body, err := json.Marshal(Report{Event: event, SessionID: in.SessionID, Source: in.Source, Message: in.Message, NotificationType: in.NotificationType})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/agent/hook", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	var c http.Client
	if hc != nil {
		c = *hc
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
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

type input struct {
	SessionID        string `json:"session_id"`
	Source           string `json:"source"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
}
