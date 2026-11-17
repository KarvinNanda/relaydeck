// Package launch builds the argv, environment and generated config files
// for starting one agent. It starts nothing and writes nothing: the caller
// resolves PATH, writes Files (mode 0600 in a 0700 dir) and runs the command.
package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/KarvinNanda/relaydeck/internal/config"
)

// ErrCswapMissing is returned when a folder has an account but cswap is not
// available. relaydeck never falls back to the default login silently.
var ErrCswapMissing = errors.New("folder has an account but cswap is not installed")

// Input is everything Build needs.
type Input struct {
	RelaydeckExe string // os.Executable()
	Workspace    config.Workspace
	Folder       config.Folder
	AgentID      string
	AgentToken   string
	DaemonURL    string
	NewSessionID string // used when Folder.SessionID is empty
	GOOS         string
	CswapPath    string // "" if not on PATH
	RuntimeDir   string // per-agent dir for generated files
	BaseEnv      []string
}

// Command is the result. Files maps full paths to contents.
type Command struct {
	Argv  []string
	Env   []string
	Dir   string
	Files map[string][]byte
}

var (
	uuidRe    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	accountRe = regexp.MustCompile(`^[A-Za-z0-9@._+-]+$`)
)

// Claude Code session markers that must not leak into a child agent.
var sessionMarkers = []string{
	"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_EXECPATH", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN",
	"CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_SESSION_ID", "CLAUDE_PID", "CLAUDE_EFFORT",
}

var hookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "Notification", "SessionEnd"}

// Build validates the input and returns the command to run.
func Build(in Input) (Command, error) {
	windows := in.GOOS == "windows"

	if strings.Contains(in.RelaydeckExe, `"`) {
		return Command{}, errors.New("relaydeck executable path contains a double quote")
	}

	// The session id goes into argv, so it must be a plain UUID.
	sessionFlag, sessionID := "--resume", in.Folder.SessionID
	if sessionID == "" {
		sessionFlag, sessionID = "--session-id", in.NewSessionID
	}
	if !uuidRe.MatchString(sessionID) {
		return Command{}, fmt.Errorf("session id %q is not a lowercase UUID", sessionID)
	}

	useCswap := false
	if !windows {
		acct := in.Folder.Account
		if strings.TrimSpace(acct) == "" && acct != "" {
			return Command{}, errors.New("account is blank")
		}
		if acct != "" {
			if strings.HasPrefix(acct, "-") || !accountRe.MatchString(acct) {
				return Command{}, fmt.Errorf("account %q has characters that are not allowed", acct)
			}
			if in.CswapPath == "" {
				return Command{}, ErrCswapMissing
			}
			useCswap = true
		}
	}

	mode := in.Folder.PermissionMode
	if mode == "" {
		mode = "auto"
	}
	mcpPath := filepath.Join(in.RuntimeDir, "mcp.json")
	settingsPath := filepath.Join(in.RuntimeDir, "settings.json")

	flags := []string{
		"--permission-mode", mode,
		"--mcp-config", mcpPath,
		"--settings", settingsPath,
		"--append-system-prompt", systemPrompt(in.Workspace, in.Folder),
		sessionFlag, sessionID,
		"--name", in.Workspace.Name + "/" + in.Folder.Name,
	}
	argv := append([]string{"claude"}, flags...)
	if useCswap {
		argv = append([]string{in.CswapPath, "run", in.Folder.Account, "--share-history", "--"}, flags...)
	}

	mcp, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"relaydeck": map[string]any{
		"type": "stdio", "command": in.RelaydeckExe, "args": []string{"mcp"},
	}}})
	if err != nil {
		return Command{}, err
	}
	hooks := map[string]any{}
	for _, ev := range hookEvents {
		hooks[ev] = []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": `"` + in.RelaydeckExe + `" hook ` + ev,
		}}}}
	}
	settings, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		return Command{}, err
	}

	return Command{
		Argv: argv,
		Env:  buildEnv(in, windows),
		Dir:  in.Folder.Path,
		Files: map[string][]byte{
			mcpPath:      mcp,
			settingsPath: settings,
		},
	}, nil
}

func buildEnv(in Input, windows bool) []string {
	norm := func(k string) string {
		if windows {
			return strings.ToUpper(k)
		}
		return k
	}
	drop := map[string]bool{}
	for _, k := range sessionMarkers {
		drop[norm(k)] = true
	}
	env := make([]string, 0, len(in.BaseEnv)+3)
	for _, e := range in.BaseEnv {
		k, _, _ := strings.Cut(e, "=")
		if drop[norm(k)] || strings.HasPrefix(norm(k), "RELAYDECK_") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"RELAYDECK_URL="+in.DaemonURL,
		"RELAYDECK_AGENT_ID="+in.AgentID,
		"RELAYDECK_AGENT_TOKEN="+in.AgentToken,
	)
}
