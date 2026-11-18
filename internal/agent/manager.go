// Package agent runs one process per folder inside a PTY, fans its output out
// to subscribers, and lets the daemon inject input.
package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/KarvinNanda/relaydeck/internal/launch"
	"github.com/KarvinNanda/relaydeck/internal/paths"
	"github.com/KarvinNanda/relaydeck/internal/screen"
	"github.com/aymanbagabas/go-pty"
)

// Status is the agent state shown in the UI.
type Status string

const (
	StatusStarting  Status = "starting"
	StatusIdle      Status = "idle"
	StatusBusy      Status = "busy"
	StatusAttention Status = "attention"
	StatusStopped   Status = "stopped"
)

// Event reports a status change. ExitCode is set only for "stopped".
type Event struct {
	AgentID  string
	Status   Status
	ExitCode *int
}

// ErrAlreadyRunning means a running agent already uses the same directory.
var ErrAlreadyRunning = errors.New("agent: a running agent already uses this directory")

// Manager owns all agents.
type Manager struct {
	log         *slog.Logger
	onUserEnter func(id string)

	mu     sync.Mutex
	agents map[string]*Agent

	evMu   sync.Mutex
	evSubs map[chan Event]struct{}
}

// NewManager creates a manager. onUserEnter (may be nil) is called each time
// the user presses Enter in an agent terminal.
func NewManager(log *slog.Logger, onUserEnter func(id string)) *Manager {
	return &Manager{
		log:         log,
		onUserEnter: onUserEnter,
		agents:      map[string]*Agent{},
		evSubs:      map[chan Event]struct{}{},
	}
}

// Start writes cmd.Files, then starts the process in a new PTY. A stopped
// agent with the same id is replaced.
func (m *Manager) Start(id string, cmd launch.Command) (*Agent, error) {
	if len(cmd.Argv) == 0 {
		return nil, errors.New("agent: empty argv")
	}
	if cmd.Env == nil {
		// A nil Env would make the child inherit everything, skipping the
		// scrub of parent Claude Code session variables.
		return nil, errors.New("agent: cmd.Env is nil, pass the scrubbed environment")
	}
	dir := filepath.Clean(cmd.Dir)
	// Compare canonical forms, so a symlink, a case difference or an NFC/NFD
	// spelling of one folder hits the guard below.
	if c, err := paths.Canonicalize(dir); err == nil {
		dir = c
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, other := range m.agents {
		if !other.isStopped() && paths.EqualNFC(other.dir, dir) {
			return nil, ErrAlreadyRunning
		}
	}
	if old, ok := m.agents[id]; ok && !old.isStopped() {
		return nil, fmt.Errorf("agent: id %q is already running", id)
	}

	path, err := exec.LookPath(cmd.Argv[0])
	if err != nil {
		return nil, err
	}
	for name, data := range cmd.Files {
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(name, data, 0o600); err != nil {
			return nil, err
		}
		// WriteFile keeps the mode of an existing file.
		if err := os.Chmod(name, 0o600); err != nil {
			return nil, err
		}
	}

	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	if err := p.Resize(defaultCols, defaultRows); err != nil {
		p.Close()
		return nil, err
	}
	c := p.Command(path, cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	if err := c.Start(); err != nil {
		p.Close()
		return nil, err
	}

	a := &Agent{
		id: id, dir: dir,
		log:         m.log,
		pty:         p,
		cmd:         c,
		ring:        newRing(scrollbackSize),
		onUserEnter: m.onUserEnter,
		emit:        m.emit,
		status:      StatusStarting,
		subs:        map[*subscriber]struct{}{},
		replyCh:     make(chan []byte, replyBuffer),
		done:        make(chan struct{}),
		readDone:    make(chan struct{}),
	}
	a.ptyResize = p.Resize
	a.reply = &replyWriter{}
	a.reply.a.Store(a)
	a.screen = screen.New(defaultCols, defaultRows, a.reply)
	m.agents[id] = a
	go a.replyLoop()
	go a.readLoop()
	go a.waitLoop()
	m.emit(Event{AgentID: id, Status: StatusStarting})
	m.log.Info("agent started", "agent_id", id)
	return a, nil
}

// Get returns the agent with this id.
func (m *Manager) Get(id string) (*Agent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	return a, ok
}

// Remove forgets a stopped agent and drops its scrollback, so the agent can
// be freed. It refuses a running agent and reports whether it was removed.
func (m *Manager) Remove(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok || !a.isStopped() {
		return false
	}
	delete(m.agents, id)
	a.ring.release()
	return true
}

// Events returns a channel of status events and a cancel function. The
// channel is buffered and sends never block: a slow consumer misses events.
func (m *Manager) Events() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	m.evMu.Lock()
	m.evSubs[ch] = struct{}{}
	m.evMu.Unlock()
	return ch, func() {
		m.evMu.Lock()
		delete(m.evSubs, ch)
		m.evMu.Unlock()
	}
}

func (m *Manager) emit(e Event) {
	m.evMu.Lock()
	defer m.evMu.Unlock()
	for ch := range m.evSubs {
		select {
		case ch <- e:
		default:
		}
	}
}

// SetStatus changes an agent's status (called by the hook handler) and emits
// an event. It is ignored for unknown or stopped agents.
func (m *Manager) SetStatus(id string, s Status) {
	a, ok := m.Get(id)
	if !ok {
		return
	}
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.status = s
	m.emit(Event{AgentID: id, Status: s})
	a.mu.Unlock()
}
