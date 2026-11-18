package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/launch"
	"github.com/KarvinNanda/relaydeck/internal/screen"
	"github.com/shirou/gopsutil/v4/process"
)

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// StartAgent starts the folder's agent. With newSession it starts a new
// session; with a sessionID it resumes that session; with neither it resumes
// the folder's current session, or starts a new one when there is none. A
// session that is open in a terminal outside relaydeck is never resumed.
// --resume needs the session file: a picked session without one is
// ErrNoTranscript, the folder's current session without one (started, then
// stopped before any prompt) gives a new session. The session used becomes
// the folder's current session in config.
//
// A user start also lifts what keeps delivery from starting this folder on
// its own (a user stop, a recent crash on start).
func (d *Daemon) StartAgent(ws, folder, sessionID string, newSession bool) error {
	if err := d.start(ws, folder, sessionID, newSession, true); err != nil {
		return err
	}
	d.life.release(delivery.Target{WS: ws, Folder: folder})
	return nil
}

func (d *Daemon) start(ws, folder, sessionID string, newSession, checkExternal bool) error {
	d.runMu.RLock()
	defer d.runMu.RUnlock()
	if d.closing {
		return ErrShuttingDown
	}
	if sessionID != "" && (newSession || !sessionIDRe.MatchString(sessionID)) {
		return fmt.Errorf("%w: session id must be a lowercase UUID and cannot be combined with new", ErrInvalid)
	}
	w, f, err := d.folderSnapshot(ws, folder)
	if err != nil {
		return err
	}
	t := delivery.Target{WS: ws, Folder: folder}
	if d.running(t) != nil {
		return ErrRunning
	}
	switch {
	case newSession:
		f.SessionID = ""
	case sessionID != "":
		if !d.hasTranscript(f.Path, sessionID) {
			return ErrNoTranscript
		}
		f.SessionID = sessionID
	case f.SessionID != "" && !d.hasTranscript(f.Path, f.SessionID):
		d.log.Info("current session has no session file, starting a new session", "ws", ws, "folder", folder)
		f.SessionID = ""
	}
	if checkExternal && f.SessionID != "" {
		open, err := d.opts.ExternalOpen(context.Background(), f.Path, f.SessionID, d.opts.OwnPIDs())
		if err != nil {
			// Same safe direction as delivery: if we cannot tell, do not resume.
			return fmt.Errorf("%w: %v", ErrExternalCheck, err)
		}
		if open {
			return ErrExternalOpen
		}
	}
	newSID := ""
	if f.SessionID == "" {
		if newSID, err = newUUID(); err != nil {
			return err
		}
	}

	id, token, err := d.reg.issue(t)
	if err != nil {
		return err
	}
	runtimeDir := filepath.Join(d.opts.RuntimeRoot, id)
	cmd, err := launch.Build(launch.Input{
		RelaydeckExe: d.opts.RelaydeckExe,
		Workspace:    w,
		Folder:       f,
		AgentID:      id,
		AgentToken:   token,
		DaemonURL:    "http://127.0.0.1:" + strconv.Itoa(d.opts.Port),
		NewSessionID: newSID,
		GOOS:         d.opts.GOOS,
		CswapPath:    d.opts.CswapPath,
		RuntimeDir:   runtimeDir,
		BaseEnv:      d.opts.BaseEnv,
	})
	if err != nil {
		d.reg.forget(id)
		return err
	}
	cmd.Files[filepath.Join(runtimeDir, ownerFile)] = []byte(strconv.Itoa(os.Getpid()))
	a, err := d.mgr.Start(id, cmd)
	if err != nil {
		d.reg.forget(id)
		os.RemoveAll(runtimeDir)
		if errors.Is(err, agent.ErrAlreadyRunning) {
			return fmt.Errorf("%w: another agent uses the same folder path", ErrRunning)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return ErrClaudeNotFound
		}
		return err
	}
	if prev := d.reg.setLatest(t, id); prev != "" {
		d.mgr.Remove(prev)
		d.reg.forget(prev)
	}
	sid := f.SessionID
	if sid == "" {
		sid = newSID
	}
	d.setSessionID(ws, folder, sid)
	d.log.Info("agent start", "agent_id", id, "ws", ws, "folder", folder, "new_session", newSID != "")
	safeGo(d.log, "watch", func() { d.watch(a, t, runtimeDir, time.Now()) })
	if d.opts.CswapPath != "" {
		// Read the default login now, so the "on default login" flag of
		// this agent is not up to 30 s old.
		safeGo(d.log, "refresh-account", func() { d.refreshAccount(context.Background()) })
	}
	return nil
}

// watch revokes the token and removes the runtime dir once the process is
// gone, whatever made it exit, and records a crash on start.
func (d *Daemon) watch(a *agent.Agent, t delivery.Target, runtimeDir string, startedAt time.Time) {
	<-a.Done()
	d.reg.revoke(a.ID())
	// Record the exit first, so the quick-exit backoff is set before the requeue
	// makes the message visible to delivery.
	d.life.exited(t, a.ID(), startedAt)
	// Messages the agent read in a turn that never reached Stop go back to queued.
	d.router.Requeue(a.ID())
	if err := os.RemoveAll(runtimeDir); err != nil {
		d.log.Warn("remove agent runtime dir failed", "agent_id", a.ID(), "error", err.Error())
	}
}

// StopAgent stops the folder's agent. A draft the user typed in the input box
// refuses a normal stop (ErrDraft), because /exit would be appended to the
// draft and submitted. Text the user never typed (a prompt suggestion) is not
// a draft. force kills at once without typing anything. Otherwise see
// stopAgent. After a user stop, delivery does not start the folder again
// for messages that were already waiting.
func (d *Daemon) StopAgent(ws, folder string, force bool) error {
	if _, _, err := d.folderSnapshot(ws, folder); err != nil {
		return err
	}
	t := delivery.Target{WS: ws, Folder: folder}
	a := d.running(t)
	if a == nil {
		return ErrNotRunning
	}
	if !force && a.HasDraft() && a.UserTyped() {
		return ErrDraft
	}
	// Before the stop, so no Tick can restart it in between: delivery does
	// not start this folder again until a new message or a user start.
	d.life.hold(t)
	d.life.markIntended(a.ID())
	var err error
	switch {
	case force:
		err = a.Kill() // no /exit: it could join a draft and be submitted
	default:
		err = d.stopAgent(a)
	}
	d.reg.revoke(a.ID())
	d.log.Info("agent stop", "agent_id", a.ID(), "ws", ws, "folder", folder, "force", force)
	return err
}

// stopAgent types /exit only into the plain input box (busy is fine), empty
// or showing a suggestion.
// With an overlay, a dialog or a permission prompt on screen, Enter would
// pick the highlighted option (trust the folder, approve a tool call), so
// the agent is killed instead.
func (d *Daemon) stopAgent(a *agent.Agent) error {
	// Text the user did not type is a prompt suggestion: typing /exit replaces
	// it (verified on Claude Code 2.1.327), and Stop checks the box shows
	// exactly "/exit" before Enter. Text the user typed would take /exit.
	if st := a.ScreenState(); st.InputVisible && !st.Trust && (st.InputEmpty || !a.UserTyped()) {
		return a.Stop(d.opts.StopGrace)
	}
	return a.Kill()
}

// Agent returns the folder's newest agent, running or stopped (a stopped one
// still has its scrollback for the terminal view).
func (d *Daemon) Agent(ws, folder string) (*agent.Agent, bool) {
	id, ok := d.reg.latest(delivery.Target{WS: ws, Folder: folder})
	if !ok {
		return nil, false
	}
	return d.mgr.Get(id)
}

// running returns the folder's agent if its process is alive.
func (d *Daemon) running(t delivery.Target) *agent.Agent {
	a, ok := d.Agent(t.WS, t.Folder)
	if !ok {
		return nil
	}
	select {
	case <-a.Done():
		return nil
	default:
		return a
	}
}

func (d *Daemon) statusOf(t delivery.Target) agent.Status {
	if a := d.running(t); a != nil {
		return a.Status()
	}
	return agent.StatusStopped
}

func (d *Daemon) statusName(ws, folder string) string {
	return string(d.statusOf(delivery.Target{WS: ws, Folder: folder}))
}

func (d *Daemon) onUserEnter(agentID string) {
	if t, ok := d.reg.owner(agentID); ok {
		d.router.UserPrompt(t.WS, t.Folder)
	}
}

// agentsAdapter is what the delivery loop sees (delivery.Agents).
type agentsAdapter struct{ d *Daemon }

var _ delivery.Agents = agentsAdapter{}

func (x agentsAdapter) Running(ws, folder string) bool {
	return x.d.running(delivery.Target{WS: ws, Folder: folder}) != nil
}

func (x agentsAdapter) Status(ws, folder string) agent.Status {
	return x.d.statusOf(delivery.Target{WS: ws, Folder: folder})
}

func (x agentsAdapter) Screen(ws, folder string) screen.State {
	if a := x.d.running(delivery.Target{WS: ws, Folder: folder}); a != nil {
		return a.ScreenState()
	}
	return screen.State{}
}

// Start is called by Tick right after ExternalOpen said no, so it does not
// check again. A folder whose agent crashed on start is not restarted for a
// while: the error makes delivery back off instead of looping.
func (x agentsAdapter) Start(ws, folder string) error {
	if x.d.life.quickExitRecently(delivery.Target{WS: ws, Folder: folder}) {
		return fmt.Errorf("agent exited within %v of its start; not restarting it automatically for %v", quickExitWindow, quickExitCooldown)
	}
	return x.d.start(ws, folder, "", false, false)
}

// ExternalOpen never checks a folder that runs in relaydeck (ruling 6).
func (x agentsAdapter) ExternalOpen(ws, folder string) (bool, error) {
	if x.Running(ws, folder) {
		return false, nil
	}
	_, f, err := x.d.folderSnapshot(ws, folder)
	if err != nil {
		return false, err
	}
	return x.d.opts.ExternalOpen(context.Background(), f.Path, f.SessionID, x.d.opts.OwnPIDs())
}

func (x agentsAdapter) UserTyped(ws, folder string) (bool, time.Time) {
	a := x.d.running(delivery.Target{WS: ws, Folder: folder})
	if a == nil {
		return false, time.Time{}
	}
	return a.TypedState()
}

func (x agentsAdapter) ClearTyped(ws, folder string, last time.Time) {
	if a := x.d.running(delivery.Target{WS: ws, Folder: folder}); a != nil {
		a.ClearTypedIf(last)
	}
}

func (x agentsAdapter) Inject(ws, folder string, p []byte) error {
	a := x.d.running(delivery.Target{WS: ws, Folder: folder})
	if a == nil {
		return agent.ErrStopped
	}
	return a.Inject(p)
}

// ownDescendants lists every process below this one (agents, and the claude
// that cswap starts under them), so the external session check never
// mistakes relaydeck's own agents for a terminal session. On error it
// returns what it found; the check then errs toward "open" (hold).
func ownDescendants() map[int]bool {
	out := map[int]bool{}
	procs, err := process.Processes()
	if err != nil {
		return out
	}
	children := map[int32][]int32{}
	for _, p := range procs {
		if pp, err := p.Ppid(); err == nil {
			children[pp] = append(children[pp], p.Pid)
		}
	}
	queue := []int32{int32(os.Getpid())}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if !out[int(c)] {
				out[int(c)] = true
				queue = append(queue, c)
			}
		}
	}
	return out
}

// newUUID returns a random lowercase UUID v4.
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
