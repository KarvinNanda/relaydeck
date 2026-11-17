// Package daemon wires the building blocks (config, agents, notify router,
// delivery loop, hooks, MCP tools, cswap) into one running relaydeck. The
// HTTP layer (internal/server) only calls the methods of *Daemon.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/cswap"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/extsession"
	"github.com/KarvinNanda/relaydeck/internal/hook"
	"github.com/KarvinNanda/relaydeck/internal/mcpserver"
	"github.com/KarvinNanda/relaydeck/internal/notify"
	"github.com/KarvinNanda/relaydeck/internal/paths"
)

// Errors returned by the Daemon methods. 13b maps them to HTTP codes.
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("already exists")
	ErrInvalid      = errors.New("invalid input")
	ErrRunning      = errors.New("agent is already running")
	ErrNotRunning   = errors.New("agent is not running")
	ErrDraft        = errors.New("agent has an unsent draft; clear it or use force")
	ErrExternalOpen = errors.New("this session is open in a terminal outside relaydeck")
	ErrShuttingDown = errors.New("relaydeck is shutting down")
)

const (
	defaultEnterDelay = 50 * time.Millisecond // spike S5: 0 works, 50 ms for safety
	defaultCswapEvery = 30 * time.Second
	defaultStopGrace  = 5 * time.Second
)

// Options configures a Daemon. DefaultOptions fills the real values; tests
// pass their own.
type Options struct {
	ConfigPath string
	Config     *config.Config // owned by the Daemon after New
	Port       int            // the port actually served (config or --port)
	Log        *slog.Logger

	RelaydeckExe string   // resolved os.Executable(), used in hook and MCP commands
	RuntimeRoot  string   // per-agent dirs are created below it
	CswapPath    string   // "" when cswap is not available (always "" on windows)
	BaseEnv      []string // environment the agent env is built from
	GOOS         string
	ClaudeHome   string // ~/.claude, for the session list

	// ExternalOpen reports whether a claude outside relaydeck has this
	// folder or session open. exclude holds relaydeck's own process ids.
	ExternalOpen func(ctx context.Context, dir, sessionID string, exclude map[int]bool) (bool, error)
	OwnPIDs      func() map[int]bool
	CswapStatus  func(ctx context.Context, path string) (cswap.Info, error)

	CswapEvery time.Duration
	EnterDelay time.Duration
	StopGrace  time.Duration
}

// DefaultOptions returns the options for a real daemon.
func DefaultOptions(configPath string, cfg *config.Config, port int, log *slog.Logger) (Options, error) {
	exe, err := os.Executable()
	if err != nil {
		return Options{}, fmt.Errorf("find relaydeck executable: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return Options{}, fmt.Errorf("resolve relaydeck executable: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Options{}, fmt.Errorf("find cache dir: %w", err)
	}
	home, err := paths.ClaudeHome()
	if err != nil {
		return Options{}, err
	}
	return Options{
		ConfigPath:   configPath,
		Config:       cfg,
		Port:         port,
		Log:          log,
		RelaydeckExe: exe,
		RuntimeRoot:  filepath.Join(cache, "relaydeck", "agents"),
		CswapPath:    cswapPath(runtime.GOOS, exec.LookPath),
		BaseEnv:      os.Environ(),
		GOOS:         runtime.GOOS,
		ClaudeHome:   home,
		// ExternalOpen, OwnPIDs and CswapStatus: New fills the real ones.
	}, nil
}

// cswapPath finds cswap on darwin and linux. Windows has no per-folder
// accounts in v1, so cswap is never used there.
func cswapPath(goos string, look func(string) (string, error)) string {
	if goos == "windows" {
		return ""
	}
	p, err := look("cswap")
	if err != nil {
		return ""
	}
	return p
}

// Daemon is the running relaydeck core.
type Daemon struct {
	opts Options
	log  *slog.Logger

	// cfgMu guards cfg for every read, mutation and Save (ruling 1). Never
	// keep a pointer from cfg.Folder across an unlock.
	cfgMu sync.Mutex
	cfg   *config.Config

	dir    directory // the one Directory shared by the router and the MCP tools
	reg    *registry
	mgr    *agent.Manager
	router *notify.Router
	deliv  *delivery.Deliverer
	hub    hub

	acctMu sync.Mutex
	acct   Account

	// runMu: starts hold it for reading; shutdown takes it for writing to
	// set closing, so no start can slip past the final stop.
	runMu   sync.RWMutex
	closing bool
}

// New builds a Daemon. It starts nothing until Run.
func New(o Options) (*Daemon, error) {
	switch {
	case o.ConfigPath == "":
		return nil, errors.New("daemon: config path is required")
	case o.Config == nil:
		return nil, errors.New("daemon: config is required")
	case o.Port <= 0 || o.Port > 65535:
		return nil, fmt.Errorf("daemon: port %d out of range", o.Port)
	case o.RelaydeckExe == "":
		return nil, errors.New("daemon: relaydeck executable path is required")
	case !filepath.IsAbs(o.RuntimeRoot):
		return nil, errors.New("daemon: runtime root must be an absolute path")
	case o.ClaudeHome == "":
		return nil, errors.New("daemon: claude home is required")
	case o.Log == nil:
		return nil, errors.New("daemon: logger is required")
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOOS == "windows" {
		o.CswapPath = ""
	}
	if o.ExternalOpen == nil {
		det := extsession.Default()
		o.ExternalOpen = func(ctx context.Context, dir, sid string, exclude map[int]bool) (bool, error) {
			_, open, err := det.Open(ctx, dir, sid, exclude)
			return open, err
		}
	}
	if o.OwnPIDs == nil {
		o.OwnPIDs = ownDescendants
	}
	if o.CswapStatus == nil {
		o.CswapStatus = cswap.Status
	}
	if o.CswapEvery <= 0 {
		o.CswapEvery = defaultCswapEvery
	}
	if o.EnterDelay <= 0 {
		o.EnterDelay = defaultEnterDelay
	}
	if o.StopGrace <= 0 {
		o.StopGrace = defaultStopGrace
	}

	d := &Daemon{opts: o, log: o.Log, cfg: o.Config, reg: newRegistry()}
	d.dir = directory{d}
	d.acct = Account{Cswap: o.CswapPath != ""}
	d.router = notify.NewRouter(d.dir, time.Now, func() string {
		id, err := randHex(8)
		if err != nil {
			panic("daemon: crypto/rand failed: " + err.Error())
		}
		return id
	})
	d.mgr = agent.NewManager(o.Log, d.onUserEnter)
	deliv, err := delivery.New(d.router, agentsAdapter{d}, o.EnterDelay, time.Now, d.targets)
	if err != nil {
		return nil, err
	}
	d.deliv = deliv
	return d, nil
}

// Tokens is the agent token registry for auth.Guard.Agents.
func (d *Daemon) Tokens() auth.AgentTokens { return d.reg }

// WhoAmI maps an agent id to its workspace and folder.
func (d *Daemon) WhoAmI(agentID string) (ws, folder string, ok bool) {
	t, ok := d.reg.owner(agentID)
	return t.WS, t.Folder, ok
}

// HookHandler serves POST /agent/hook. Mount it behind auth.Guard.Agent.
func (d *Daemon) HookHandler() http.Handler { return hook.Handler(d.onHook) }

// ToolsHandler serves POST /agent/tools/{name}. Mount it behind
// auth.Guard.Agent. It uses the same Directory as the router (ruling 4).
func (d *Daemon) ToolsHandler() http.Handler {
	return mcpserver.Handlers(d.router, d.dir, d.statusName, d.WhoAmI)
}

// Run starts the delivery loop, the event fan-in and the cswap poll, and
// blocks until ctx ends. Then it stops every agent and returns.
func (d *Daemon) Run(ctx context.Context) {
	evs, cancelEvs := d.mgr.Events()
	msgs, cancelMsgs := d.router.Subscribe()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); d.deliv.Run(ctx) }()
	go func() { defer wg.Done(); d.fanIn(ctx, evs, msgs) }()
	go func() { defer wg.Done(); d.pollCswap(ctx) }()
	<-ctx.Done()
	wg.Wait()
	cancelEvs()
	cancelMsgs()
	d.stopAll()
}

// fanIn turns agent and message events into daemon events and kicks the
// delivery loop on every one of them (ruling 6).
func (d *Daemon) fanIn(ctx context.Context, evs <-chan agent.Event, msgs <-chan notify.Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-evs:
			if t, ok := d.reg.owner(e.AgentID); ok {
				d.hub.publish(Event{Type: "status", WS: t.WS, Folder: t.Folder, Status: e.Status, ExitCode: e.ExitCode})
			}
			d.deliv.Kick()
		case m, ok := <-msgs:
			if !ok {
				return
			}
			d.hub.publish(Event{Type: "message", WS: m.Workspace, Folder: m.To, Message: &m})
			d.deliv.Kick()
		}
	}
}

// stopAll refuses new starts, then stops every running agent in parallel
// (ruling 16). An agent with a draft is killed at once: typing /exit after a
// draft would submit "draft/exit" as a prompt (Task 8 carry).
func (d *Daemon) stopAll() {
	d.runMu.Lock()
	d.closing = true
	d.runMu.Unlock()

	var wg sync.WaitGroup
	for _, t := range d.targets() {
		a := d.running(t)
		if a == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop := func() error { return a.Stop(d.opts.StopGrace) }
			if a.HasDraft() {
				stop = a.Kill
			}
			if err := stop(); err != nil {
				d.log.Error("stop agent failed", "agent_id", a.ID(), "ws", t.WS, "folder", t.Folder, "error", err.Error())
			}
			d.reg.revoke(a.ID())
		}()
	}
	wg.Wait()
}
