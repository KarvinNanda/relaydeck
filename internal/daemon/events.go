package daemon

import (
	"sync"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/notify"
)

// Event is one change for the UI event stream.
//   - "status":  WS, Folder, Status (ExitCode when stopped)
//   - "message": WS, Folder (the target), Message
//   - "account": Account
type Event struct {
	Type     string          `json:"type"`
	WS       string          `json:"ws,omitempty"`
	Folder   string          `json:"folder,omitempty"`
	Status   agent.Status    `json:"status,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
	Message  *notify.Message `json:"message,omitempty"`
	Account  *Account        `json:"account,omitempty"`
}

const eventBuffer = 64

type hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// Subscribe returns a channel of events and a cancel func (safe to call
// twice; it closes the channel). Sends never block: a slow consumer misses
// events and should re-read State (ruling 11 is handled in 13b).
func (d *Daemon) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, eventBuffer)
	d.hub.mu.Lock()
	if d.hub.subs == nil {
		d.hub.subs = map[chan Event]struct{}{}
	}
	d.hub.subs[ch] = struct{}{}
	d.hub.mu.Unlock()
	return ch, func() {
		d.hub.mu.Lock()
		defer d.hub.mu.Unlock()
		if _, ok := d.hub.subs[ch]; ok {
			delete(d.hub.subs, ch)
			close(ch)
		}
	}
}

func (h *hub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
