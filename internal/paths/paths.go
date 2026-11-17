// Package paths makes folder paths canonical and derives the directory name
// Claude Code uses under ~/.claude/projects for a working directory.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ErrNotDir is returned when a path does not exist or is not a directory.
var ErrNotDir = errors.New("path is not an existing directory")

// maxDirNameUnits is the longest sanitized name Claude Code keeps as is.
const maxDirNameUnits = 200

// Canonicalize returns the absolute, symlink-free path of an existing
// directory, with letter case fixed to the on-disk case on darwin and windows.
// A leading "~" is expanded to the home directory.
func Canonicalize(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		p = filepath.Join(home, p[1:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("make %q absolute: %w", p, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotDir, abs)
	}
	fixed, err := fixCase(resolved)
	if err != nil {
		return "", fmt.Errorf("fix case of %q: %w", resolved, err)
	}
	info, err := os.Stat(fixed)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrNotDir, fixed)
	}
	return fixed, nil
}

// walkCase rebuilds p one component at a time, replacing each component with
// the matching directory entry's on-disk spelling. An exact match wins over a
// case-insensitive one. A component with no match is kept as given.
func walkCase(p string) (string, error) {
	vol := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p, vol)
	cur := vol + string(filepath.Separator)
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", err
		}
		name := part
		folded := ""
		for _, e := range entries {
			if e.Name() == part {
				folded = ""
				break
			}
			if folded == "" && strings.EqualFold(e.Name(), part) {
				folded = e.Name()
			}
		}
		if folded != "" {
			name = folded
		}
		cur = filepath.Join(cur, name)
	}
	return cur, nil
}

// ProjectDirName returns the directory name Claude Code uses under
// ~/.claude/projects for the working directory canonical. Every UTF-16 code
// unit outside [A-Za-z0-9] becomes "-". When the result is longer than 200
// units, it is cut to 200 and "-" plus a base36 hash of the original path is
// appended. The hash is a Java-style int32 string hash over UTF-16 units.
func ProjectDirName(canonical string) string {
	units := utf16.Encode([]rune(canonical))
	out := make([]uint16, len(units))
	for i, u := range units {
		if (u >= 'a' && u <= 'z') || (u >= 'A' && u <= 'Z') || (u >= '0' && u <= '9') {
			out[i] = u
		} else {
			out[i] = '-'
		}
	}
	if len(out) <= maxDirNameUnits {
		return string(utf16.Decode(out))
	}
	var h int32
	for _, u := range units {
		h = 31*h + int32(u)
	}
	abs := int64(h)
	if abs < 0 {
		abs = -abs
	}
	return string(utf16.Decode(out[:maxDirNameUnits])) + "-" + strconv.FormatInt(abs, 36)
}

// ClaudeHome returns Claude Code's config directory: CLAUDE_CONFIG_DIR when it
// is set and not empty, otherwise ~/.claude.
func ClaudeHome() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}
