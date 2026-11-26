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
//   - "external": WS, Folder (an outside claude opened or closed)
//   - "config":  nothing (a workspace or folder was added or removed)
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
// twice; it closes the channel). Sends never block: a consumer whose buffer
// is full is dropped and its channel closed, so it knows it missed events
// and can start again from a State snapshot (ruling 11). Run closes every
// channel when it returns.
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
			delete(h.subs, ch)
			close(ch)
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}
