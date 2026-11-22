// Package extsession finds Claude Code sessions that run outside relaydeck.
// Before relaydeck resumes a session it asks Detector.Open whether another
// interactive claude process already has that session or folder open.
package extsession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/paths"
	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/text/unicode/norm"
)

// commandTimeout bounds the `claude agents --json` call.
const commandTimeout = 3 * time.Second

// agentsArgv is the only argv this package passes to claude. It never
// contains user input.
var agentsArgv = []string{"agents", "--json"}

// Session is one running claude process.
type Session struct {
	PID       int
	Cwd       string
	SessionID string
	Kind      string
}

// Detector looks for claude sessions. Both functions are injected so tests
// never run claude.
type Detector struct {
	Agents func(ctx context.Context) ([]Session, error)
	Procs  func() ([]Session, error)
}

// Default returns a Detector that runs `claude agents --json` and falls back
// to scanning the process list.
func Default() Detector {
	return Detector{Agents: runAgents, Procs: ListProcs}
}

// nonInteractive lists the kinds `claude agents --json` reports for sessions
// nobody can type into. Any other kind, including an empty or new one, counts
// as interactive: an unknown kind cannot be ruled out, so it holds delivery.
var nonInteractive = map[string]bool{"background": true}

// Snapshot is one reading of the running claude processes. It answers Open
// for any number of folders, so a poll over many agents runs the source once.
type Snapshot struct {
	sessions []Session
}

// Snapshot reads the running sessions with Agents, or with Procs when Agents
// fails (Procs knows no session ids, so it can only match by folder). If both
// fail the error is returned and the caller must treat sessions as open.
func (d Detector) Snapshot(ctx context.Context) (Snapshot, error) {
	var sessions []Session
	var agentsErr error
	if d.Agents != nil {
		sessions, agentsErr = d.Agents(ctx)
	} else {
		agentsErr = errors.New("no agents source")
	}
	if agentsErr != nil {
		if d.Procs == nil {
			return Snapshot{}, agentsErr
		}
		var procsErr error
		sessions, procsErr = d.Procs()
		if procsErr != nil {
			return Snapshot{}, fmt.Errorf("detect external sessions: agents: %v; procs: %w", agentsErr, procsErr)
		}
	}
	return Snapshot{sessions: sessions}, nil
}

// Open reports the pid of an interactive claude session in the snapshot that
// has sessionID or dir open. Processes in exclude (relaydeck's own children)
// are ignored. An unknown cwd cannot be ruled out, so it counts as open.
func (s Snapshot) Open(dir, sessionID string, exclude map[int]bool) (int, bool) {
	for _, x := range s.sessions {
		if nonInteractive[x.Kind] || exclude[x.PID] {
			continue
		}
		if (sessionID != "" && x.SessionID == sessionID) || x.Cwd == "" || samePath(x.Cwd, dir) {
			return x.PID, true
		}
	}
	return 0, false
}

// OpenKnown is Open without the "unknown cwd counts as open" rule: only a
// session id or the same path matches. The poll for running agents uses it,
// because its answer holds delivery, and a process whose cwd could not be
// read would otherwise hold every folder.
func (s Snapshot) OpenKnown(dir, sessionID string, exclude map[int]bool) (int, bool) {
	for _, x := range s.sessions {
		if nonInteractive[x.Kind] || exclude[x.PID] {
			continue
		}
		if (sessionID != "" && x.SessionID == sessionID) || (x.Cwd != "" && samePath(x.Cwd, dir)) {
			return x.PID, true
		}
	}
	return 0, false
}

// Open reports whether an interactive claude session outside relaydeck has
// sessionID or dir open: a Snapshot and its Open in one call. If the
// snapshot fails the error is returned and the caller must treat the session
// as open.
func (d Detector) Open(ctx context.Context, dir, sessionID string, exclude map[int]bool) (int, bool, error) {
	snap, err := d.Snapshot(ctx)
	if err != nil {
		return 0, false, err
	}
	pid, open := snap.Open(dir, sessionID, exclude)
	return pid, open, nil
}

// samePath compares two paths after cleaning, and again after resolving
// symlinks (when resolution fails the cleaned path is used). Either form
// matching is enough. darwin and windows are treated as case-insensitive.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if equalPath(a, b) {
		return true
	}
	return equalPath(resolve(a), resolve(b))
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return p
}

func equalPath(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(norm.NFC.String(a), norm.NFC.String(b))
	}
	return paths.EqualNFC(a, b)
}

type agentJSON struct {
	Kind      string `json:"kind"`
	Cwd       string `json:"cwd"`
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
}

// ParseAgents parses the output of `claude agents --json`.
func ParseAgents(data []byte) ([]Session, error) {
	var raw []agentJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse claude agents output: %w", err)
	}
	out := make([]Session, 0, len(raw))
	for _, a := range raw {
		out = append(out, Session{PID: a.PID, Cwd: a.Cwd, SessionID: a.SessionID, Kind: a.Kind})
	}
	return out, nil
}

func runAgents(ctx context.Context) ([]Session, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "claude", agentsArgv...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run claude agents: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseAgents(stdout.Bytes())
}

// ListProcs scans the process list for claude binaries and reports each one
// with its working directory. It cannot know the session id. A claude process
// whose cwd cannot be read is kept with an empty Cwd, which Open treats as
// open.
func ListProcs() ([]Session, error) {
	ps, err := process.Processes()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	var out []Session
	for _, p := range ps {
		name, _ := p.Name()
		exe, exeErr := p.Exe()
		if !classify(name, exe, exeErr) {
			continue
		}
		// A zombie (exited, not reaped yet) is no session anyone can type
		// into, and its cwd cannot be read, which would make every folder
		// look open (#109).
		if st, err := p.Status(); err == nil && slices.Contains(st, process.Zombie) {
			continue
		}
		cwd, cwdErr := p.Cwd()
		out = append(out, toSession(int(p.Pid), cwd, cwdErr))
	}
	return out, nil
}

// toSession builds a Session for a claude process. An unreadable cwd becomes
// an empty Cwd instead of dropping the process.
func toSession(pid int, cwd string, cwdErr error) Session {
	if cwdErr != nil {
		cwd = ""
	}
	return Session{PID: pid, Cwd: cwd, Kind: "interactive"}
}

var versionNameRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// classify says whether a process may be claude. The native build reports its
// version string as the process name, so when the exe path cannot be read and
// the name looks like a version, it is treated as claude (cannot rule out).
func classify(name, exe string, exeErr error) bool {
	if exeErr == nil && (strings.Contains(exe, "/claude/versions/") || strings.Contains(exe, `\claude\versions\`)) {
		return true
	}
	if name == "claude" || name == "claude.exe" {
		return true
	}
	return exeErr != nil && versionNameRe.MatchString(name)
}
