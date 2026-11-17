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
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
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

// Open reports whether an interactive claude session outside relaydeck has
// sessionID or dir open. Processes in exclude (relaydeck's own children) are
// ignored. If Agents fails, Procs is used, and it can only match by folder.
// If both fail the error is returned and the caller must treat the session as
// open.
func (d Detector) Open(ctx context.Context, dir, sessionID string, exclude map[int]bool) (int, bool, error) {
	var sessions []Session
	var agentsErr error
	if d.Agents != nil {
		sessions, agentsErr = d.Agents(ctx)
	} else {
		agentsErr = errors.New("no agents source")
	}
	if agentsErr != nil {
		if d.Procs == nil {
			return 0, false, agentsErr
		}
		var procsErr error
		sessions, procsErr = d.Procs()
		if procsErr != nil {
			return 0, false, fmt.Errorf("detect external sessions: agents: %v; procs: %w", agentsErr, procsErr)
		}
	}
	for _, s := range sessions {
		if s.Kind != "interactive" || exclude[s.PID] {
			continue
		}
		if (sessionID != "" && s.SessionID == sessionID) || (s.Cwd != "" && samePath(s.Cwd, dir)) {
			return s.PID, true, nil
		}
	}
	return 0, false, nil
}

// samePath compares two paths after cleaning. darwin and windows file systems
// are treated as case-insensitive.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
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
		return nil, fmt.Errorf("run claude agents: %w", err)
	}
	return ParseAgents(stdout.Bytes())
}

// ListProcs scans the process list for claude binaries and reports each one
// with its working directory. It cannot know the session id.
func ListProcs() ([]Session, error) {
	ps, err := process.Processes()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	var out []Session
	for _, p := range ps {
		if !looksLikeClaude(p) {
			continue
		}
		cwd, err := p.Cwd()
		if err != nil || cwd == "" {
			continue
		}
		out = append(out, Session{PID: int(p.Pid), Cwd: cwd, Kind: "interactive"})
	}
	return out, nil
}

func looksLikeClaude(p *process.Process) bool {
	if exe, err := p.Exe(); err == nil {
		if strings.Contains(exe, "/claude/versions/") || strings.Contains(exe, `\claude\versions\`) {
			return true
		}
	}
	name, err := p.Name()
	return err == nil && (name == "claude" || name == "claude.exe")
}
