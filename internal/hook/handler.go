package hook

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/KarvinNanda/relaydeck/internal/auth"
)

const maxBody = 64 << 10

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// capRunes cuts s to at most n bytes without splitting a UTF-8 character.
func capRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// shortASCII returns s if it is at most 64 printable ASCII characters, else "".
func shortASCII(s string) string {
	if len(s) > 64 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return ""
		}
	}
	return s
}

// StatusFor maps a report to an agent status. ok is false for an unknown event.
func StatusFor(r Report) (status string, ok bool) {
	switch r.Event {
	case "SessionStart", "Stop":
		return "idle", true
	case "UserPromptSubmit":
		return "busy", true
	case "Notification":
		// idle_prompt fires 60 s after Stop and is the only signal after an Esc cancel.
		if r.NotificationType == "idle_prompt" {
			return "idle", true
		}
		return "attention", true
	case "SessionEnd":
		return "stopped", true
	}
	return "", false
}

// Handler parses and validates a hook report, then calls onEvent with the agent id
// that the auth guard put in the context. Mount it behind Guard.Agent.
func Handler(onEvent func(agentID string, r Report)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id, ok := auth.AgentID(req.Context())
		if !ok || id == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var r Report
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBody)).Decode(&r); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !knownEvent(r.Event) {
			http.Error(w, "unknown event", http.StatusBadRequest)
			return
		}
		// The id reaches --resume argv later, so only a canonical lowercase UUID is kept.
		if !uuidRE.MatchString(r.SessionID) {
			r.SessionID = ""
		}
		r.Message = capRunes(r.Message, maxMessage)
		r.Source = shortASCII(r.Source)
		r.NotificationType = shortASCII(r.NotificationType)
		onEvent(id, r)
		w.WriteHeader(http.StatusNoContent)
	})
}
