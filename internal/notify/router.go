// Package notify is the in-memory message store and hop-limit logic for agent
// to agent messages. It never touches processes or terminals.
package notify

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type State string

const (
	StateQueued           State = "queued"
	StateHeldExternal     State = "held_external"
	StateHeldDraft        State = "held_draft"
	StateNudged           State = "nudged"
	StateAwaitingApproval State = "awaiting_approval"
	// StateDelivered is set only by Read (the agent really pulled it), never by typing the nudge.
	StateDelivered State = "delivered"
	StateRejected  State = "rejected"
)

const (
	MaxBody  = 16 << 10
	HopLimit = 3
	// MaxPendingPerTarget caps unread messages (including held ones) per target agent.
	MaxPendingPerTarget = 50
	// MaxFinishedPerWorkspace caps delivered and rejected messages kept per workspace.
	// The oldest are dropped when a new message arrives. Pending messages are never dropped.
	MaxFinishedPerWorkspace = 500
)

type Message struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Body      string    `json:"body"`
	Hop       int       `json:"hop"`
	Created   time.Time `json:"created"`
	State     State     `json:"state"`

	// reading is set while the agent has read the message but its turn has not
	// ended (see Confirm and Requeue). Such a message is never pruned.
	reading bool
}

// Errors of Approve and Reject, so the HTTP layer can tell 404 from 409.
var (
	ErrUnknownMessage      = errors.New("unknown message")
	ErrNotAwaitingApproval = errors.New("not awaiting_approval")
)

// Directory lists the valid folder names of a workspace.
type Directory interface{ Folders(ws string) []string }

type agentKey struct{ ws, agent string }

type Router struct {
	dir   Directory
	now   func() time.Time
	newID func() string

	mu      sync.Mutex
	msgs    []*Message // creation order
	byID    map[string]*Message
	hop     map[agentKey]int
	reads   map[string][]*Message // by agent id: read since that agent's last turn start, not yet confirmed
	subs    map[int]chan Message
	nextSub int
}

func NewRouter(dir Directory, now func() time.Time, newID func() string) *Router {
	return &Router{
		dir: dir, now: now, newID: newID,
		byID:  map[string]*Message{},
		hop:   map[agentKey]int{},
		reads: map[string][]*Message{},
		subs:  map[int]chan Message{},
	}
}

// Send validates and stores a message. The sender is not checked against the
// directory: the caller derives it from the agent token.
func (r *Router) Send(ws, from, to, body string) (Message, error) {
	names := append([]string(nil), r.dir.Folders(ws)...)
	sort.Strings(names)
	known := false
	for _, n := range names {
		if n == to {
			known = true
			break
		}
	}
	if to == "" || !known {
		return Message{}, fmt.Errorf("unknown folder %q; valid: %s", to, strings.Join(names, ", "))
	}
	if to == from {
		return Message{}, errors.New("cannot notify yourself")
	}
	if body == "" {
		return Message{}, errors.New("body is empty")
	}
	if len(body) > MaxBody {
		return Message{}, fmt.Errorf("body is %d bytes, the limit is %d", len(body), MaxBody)
	}
	if !utf8.ValidString(body) {
		return Message{}, errors.New("body is not valid UTF-8")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	pending := 0
	for _, o := range r.msgs {
		if o.Workspace == ws && o.To == to && (inFlight(o.State) || o.State == StateAwaitingApproval) {
			pending++
		}
	}
	if pending >= MaxPendingPerTarget {
		return Message{}, fmt.Errorf("%q has %d unread messages; wait until it reads them", to, MaxPendingPerTarget)
	}
	hop := r.hop[agentKey{ws, from}] + 1
	state := StateQueued
	if hop >= HopLimit {
		state = StateAwaitingApproval
	}
	m := &Message{Workspace: ws, From: from, To: to, Body: body, Hop: hop, State: state}
	m.Created = r.now()
	m.ID = r.newID()
	r.msgs = append(r.msgs, m)
	r.byID[m.ID] = m
	r.prune(ws)
	r.publish(*m)
	return *m, nil
}

// prune drops the oldest delivered or rejected messages of ws beyond
// MaxFinishedPerWorkspace. Messages still being read are kept. Caller holds r.mu.
func (r *Router) prune(ws string) {
	finished := 0
	for _, m := range r.msgs {
		if m.Workspace == ws && finishedState(m.State) {
			finished++
		}
	}
	excess := finished - MaxFinishedPerWorkspace
	if excess <= 0 {
		return
	}
	kept := r.msgs[:0]
	for _, m := range r.msgs {
		if excess > 0 && m.Workspace == ws && finishedState(m.State) && !m.reading {
			delete(r.byID, m.ID)
			excess--
			continue
		}
		kept = append(kept, m)
	}
	for i := len(kept); i < len(r.msgs); i++ {
		r.msgs[i] = nil
	}
	r.msgs = kept
}

func finishedState(s State) bool { return s == StateDelivered || s == StateRejected }

// markRead marks m delivered and, with an agent id, remembers it for Confirm
// and Requeue. Caller holds r.mu.
func (r *Router) markRead(agentID string, m *Message) {
	m.State = StateDelivered
	if agentID == "" {
		return
	}
	m.reading = true
	r.reads[agentID] = append(r.reads[agentID], m)
}

// Confirm ends the turn of agent process agentID as far as reading goes: the
// messages it read are final and will not be requeued. Call it on the Stop
// hook and on a new UserPromptSubmit.
func (r *Router) Confirm(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.reads[agentID] {
		m.reading = false
	}
	delete(r.reads, agentID)
}

// Requeue is for agent process agentID that exited before its turn ended: every
// message it read since the last Confirm goes back to queued, unless it was
// rejected meanwhile. The hop counter is left as it is.
func (r *Router) Requeue(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.reads[agentID] {
		m.reading = false
		if m.State == StateDelivered {
			m.State = StateQueued
			r.publish(*m)
		}
	}
	delete(r.reads, agentID)
}

// Read is ReadN without a budget and without read tracking (no agent id): the
// messages are final at once. Production code uses ReadAs.
func (r *Router) Read(ws, agent string) []Message {
	msgs, _ := r.ReadAs(ws, agent, "", 1<<62)
	return msgs
}

// ReadN is ReadAs without read tracking.
func (r *Router) ReadN(ws, agent string, maxBytes int) (msgs []Message, remaining int) {
	return r.ReadAs(ws, agent, "", maxBytes)
}

// ReadAs returns the oldest pending (queued, held_draft, nudged) messages of
// folder `agent` while the sum of len(Envelope(m)) stays within maxBytes (always
// at least one), marks only those delivered, and reports how many remain pending
// for the next call. The caller's handling hop becomes the highest hop it read.
//
// With a non-empty agentID the messages stay requeueable until Confirm(agentID)
// (see Requeue). The id is the running agent process, not the folder, so an old
// process can never requeue what a newer process of the same folder read.
func (r *Router) ReadAs(ws, agent, agentID string, maxBytes int) (msgs []Message, remaining int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	used, maxHop := 0, 0
	full := false
	for _, m := range r.msgs {
		if m.Workspace != ws || m.To != agent {
			continue
		}
		if m.State != StateQueued && m.State != StateHeldDraft && m.State != StateNudged {
			continue
		}
		size := len(Envelope(*m))
		if full || (len(msgs) > 0 && used+size > maxBytes) {
			// Strict FIFO: after the first message that does not fit, nothing later is taken.
			full = true
			remaining++
			continue
		}
		used += size
		r.markRead(agentID, m)
		if m.Hop > maxHop {
			maxHop = m.Hop
		}
		msgs = append(msgs, *m)
		r.publish(*m)
	}
	if len(msgs) > 0 {
		r.hop[agentKey{ws, agent}] = maxHop
	}
	return msgs, remaining
}

// UserPrompt records that the user typed in the agent's panel: a new turn, hop 0.
func (r *Router) UserPrompt(ws, agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hop, agentKey{ws, agent})
}

// Pending lists messages to the agent that are not yet read, oldest first.
func (r *Router) Pending(ws, agent string) []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Message
	for _, m := range r.msgs {
		if m.Workspace != ws || m.To != agent {
			continue
		}
		switch m.State {
		case StateQueued, StateHeldExternal, StateHeldDraft, StateNudged:
			out = append(out, *m)
		}
	}
	return out
}

// inFlight reports whether s is a state a message can be in while waiting to be read.
func inFlight(s State) bool {
	switch s {
	case StateQueued, StateHeldExternal, StateHeldDraft, StateNudged:
		return true
	}
	return false
}

// SetState moves a message between the in-flight states (queued, held_external,
// held_draft, nudged). It refuses any other current or target state: delivered is
// reachable only through Read, queued from awaiting_approval only through Approve,
// rejected only through Reject. This keeps a stale snapshot from reviving a
// delivered message or skipping the user's approval.
func (r *Router) SetState(id string, s State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("unknown message %q", id)
	}
	if !inFlight(m.State) || !inFlight(s) {
		return fmt.Errorf("cannot change message %q from %s to %s", id, m.State, s)
	}
	m.State = s
	r.publish(*m)
	return nil
}

// Approve moves an awaiting_approval message to queued.
func (r *Router) Approve(id string) error { return r.resolve(id, StateQueued) }

// Reject moves an awaiting_approval message to rejected.
func (r *Router) Reject(id string) error { return r.resolve(id, StateRejected) }

func (r *Router) resolve(id string, to State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownMessage, id)
	}
	if m.State != StateAwaitingApproval {
		return fmt.Errorf("message %q is %s, %w", id, m.State, ErrNotAwaitingApproval)
	}
	m.State = to
	r.publish(*m)
	return nil
}

// Timeline returns every message of the workspace, oldest first.
func (r *Router) Timeline(ws string) []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Message
	for _, m := range r.msgs {
		if m.Workspace == ws {
			out = append(out, *m)
		}
	}
	return out
}

// Subscribe returns a channel (capacity 64) that gets a copy of every created or
// changed message. A full channel misses events; the subscriber can re-read Timeline.
// The cancel func removes and closes the channel and is safe to call twice.
func (r *Router) Subscribe() (<-chan Message, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextSub
	r.nextSub++
	ch := make(chan Message, 64)
	r.subs[id] = ch
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if c, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(c)
		}
	}
}

// publish must be called with r.mu held.
func (r *Router) publish(m Message) {
	for _, ch := range r.subs {
		select {
		case ch <- m:
		default:
		}
	}
}
