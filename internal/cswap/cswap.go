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
	// DefaultEmail is the email shown after the number, "" if none is shown.
	DefaultEmail string
}

var (
	statusRe  = regexp.MustCompile(`(?m)^Status: Account-(\d+) \(([^)\s]*)`)
	accountRe = regexp.MustCompile(`^Account-(\d+)$`)
	emailRe   = regexp.MustCompile(`^[^@\s/]+@[^@\s/]+\.[^@\s/]+$`)
)

func run(ctx context.Context, path string, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, argv...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run cswap %s: %w: %s", argv[0], err, strings.TrimSpace(stderr.String()))
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
	info := Info{DefaultAccount: m[1]}
	if emailRe.MatchString(m[2]) {
		info.DefaultEmail = m[2]
	}
	return info, nil
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

// parseMap looks for a line that holds dir as a whole path and, on the same
// line, an Account-N token or an email. The list format is not documented, so
// any other line is skipped. The path is searched in its native form and with
// forward slashes.
func parseMap(out, dir string) (string, bool) {
	dir = filepath.Clean(dir)
	cands := []string{dir}
	if s := filepath.ToSlash(dir); s != dir {
		cands = append(cands, s)
	}
	fold := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	for _, line := range strings.Split(out, "\n") {
		for _, c := range cands {
			i := findPath(line, c, fold)
			if i < 0 {
				continue
			}
			rest := line[:i] + " " + line[i+len(c):]
			for _, f := range strings.Fields(rest) {
				f = strings.Trim(f, "()[],;:\"'")
				if m := accountRe.FindStringSubmatch(f); m != nil {
					return m[1], true
				}
				if emailRe.MatchString(f) {
					return f, true
				}
			}
		}
	}
	return "", false
}

// findPath returns the offset of path in line when it appears as a whole
// path, not as part of a longer one, or -1.
func findPath(line, path string, fold bool) int {
	for from := 0; from <= len(line)-len(path); {
		i := indexFold(line[from:], path, fold)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(path)
		okEnd := end == len(line) || strings.ContainsRune(" \t\"'),;:", rune(line[end]))
		okStart := i == 0 || strings.ContainsRune(" \t\"'(", rune(line[i-1]))
		if okEnd && okStart {
			return i
		}
		from = i + 1
	}
	return -1
}

// indexFold is strings.Index, optionally ignoring case. The offset is in h
// itself, unlike searching a lowercased copy. The match must have the same
// byte length as n.
func indexFold(h, n string, fold bool) int {
	if !fold {
		return strings.Index(h, n)
	}
	for i := 0; i+len(n) <= len(h); i++ {
		if strings.EqualFold(h[i:i+len(n)], n) {
			return i
		}
	}
	return -1
}
