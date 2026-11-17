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
)

type Message struct {
	ID, Workspace, From, To, Body string
	Hop                           int
	Created                       time.Time
	State                         State
}

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
	subs    map[int]chan Message
	nextSub int
}

func NewRouter(dir Directory, now func() time.Time, newID func() string) *Router {
	return &Router{
		dir: dir, now: now, newID: newID,
		byID: map[string]*Message{},
		hop:  map[agentKey]int{},
		subs: map[int]chan Message{},
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
	r.publish(*m)
	return *m, nil
}

// Read returns the caller's queued, held_draft and nudged messages and marks them
// delivered. The caller's handling hop becomes the highest hop it read.
func (r *Router) Read(ws, agent string) []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Message
	maxHop := 0
	for _, m := range r.msgs {
		if m.Workspace != ws || m.To != agent {
			continue
		}
		if m.State != StateQueued && m.State != StateHeldDraft && m.State != StateNudged {
			continue
		}
		m.State = StateDelivered
		if m.Hop > maxHop {
			maxHop = m.Hop
		}
		out = append(out, *m)
		r.publish(*m)
	}
	if len(out) > 0 {
		r.hop[agentKey{ws, agent}] = maxHop
	}
	return out
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

func (r *Router) SetState(id string, s State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("unknown message %q", id)
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
		return fmt.Errorf("unknown message %q", id)
	}
	if m.State != StateAwaitingApproval {
		return fmt.Errorf("message %q is %s, not awaiting_approval", id, m.State)
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
