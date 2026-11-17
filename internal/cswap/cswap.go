// Package cswap reads claude-swap (cswap) state. It only runs read-only
// commands, from a fixed allowlist.
package cswap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const commandTimeout = 3 * time.Second

// The only argv this package ever passes to cswap. No user input goes in.
var (
	statusArgv = []string{"status"}
	mapArgv    = []string{"map"}
)

// ErrUnknownFormat means cswap printed something this package cannot parse.
var ErrUnknownFormat = errors.New("unknown cswap output format")

// Info is what `cswap status` tells us.
type Info struct {
	// DefaultAccount is the account number of the default login.
	DefaultAccount string
}

var (
	statusRe  = regexp.MustCompile(`(?m)^Status: Account-(\d+) \(`)
	accountRe = regexp.MustCompile(`^Account-(\d+)$`)
)

func run(ctx context.Context, path string, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, path, argv...)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run cswap %s: %w", argv[0], err)
	}
	return stdout.String(), nil
}

// Status runs `cswap status` and returns the default account.
func Status(ctx context.Context, path string) (Info, error) {
	out, err := run(ctx, path, statusArgv)
	if err != nil {
		return Info{}, err
	}
	return parseStatus(out)
}

func parseStatus(out string) (Info, error) {
	m := statusRe.FindStringSubmatch(out)
	if m == nil {
		return Info{}, ErrUnknownFormat
	}
	return Info{DefaultAccount: m[1]}, nil
}

// MappedAccount runs `cswap map` with no arguments and returns the account
// mapped to dir, if the output says so clearly.
func MappedAccount(ctx context.Context, path, dir string) (string, bool, error) {
	out, err := run(ctx, path, mapArgv)
	if err != nil {
		return "", false, err
	}
	acc, ok := parseMap(out, dir)
	return acc, ok, nil
}

// parseMap looks for a line that holds dir as a whole path and an account
// number (Account-N or a bare number) or an email. The list format is not
// documented, so any other line is skipped.
func parseMap(out, dir string) (string, bool) {
	dir = filepath.Clean(dir)
	fold := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if fold {
		dir = strings.ToLower(dir)
	}
	for _, line := range strings.Split(out, "\n") {
		hay := line
		if fold {
			hay = strings.ToLower(line)
		}
		i := strings.Index(hay, dir)
		if i < 0 {
			continue
		}
		end := i + len(dir)
		if end < len(hay) && !strings.ContainsRune(" \t\"'),;:", rune(hay[end])) {
			continue
		}
		if i > 0 && !strings.ContainsRune(" \t\"'(", rune(hay[i-1])) {
			continue
		}
		rest := line[:i] + " " + line[end:]
		for _, f := range strings.Fields(rest) {
			f = strings.Trim(f, "()[],;:\"'")
			if m := accountRe.FindStringSubmatch(f); m != nil {
				return m[1], true
			}
			if isNumber(f) || strings.Contains(f, "@") {
				return f, true
			}
		}
	}
	return "", false
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
