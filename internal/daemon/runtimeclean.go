package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// ownerFile is written into each per-agent runtime dir with the daemon's pid.
// CleanStaleRuntime leaves a dir alone while that process is alive, so a
// second relaydeck on another port does not lose its live agents' files.
const ownerFile = "owner.pid"

// writeOwner records this process as the owner of a runtime dir.
func writeOwner(dir string) error {
	return os.WriteFile(filepath.Join(dir, ownerFile), []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// CleanStaleRuntime removes per-agent runtime dirs left behind by a daemon
// that was killed. It only touches direct children of root whose name is an
// agent id (a UUID) and that are real directories: a symlink, a file or any
// other name is left alone, and nothing is followed outside root. A dir whose
// owner.pid names another live process is kept. It returns how many dirs it
// removed.
func CleanStaleRuntime(root string, log *slog.Logger) int {
	if !filepath.IsAbs(root) {
		return 0
	}
	// root itself must be a real directory, not a symlink to somewhere else.
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() {
		return 0
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Warn("list runtime dir failed", "error", err.Error())
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || !sessionIDRe.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if ownerAlive(dir) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("remove stale runtime dir failed", "agent_id", e.Name(), "error", err.Error())
			continue
		}
		removed++
		log.Info("removed stale runtime dir", "agent_id", e.Name())
	}
	return removed
}

// ownerAlive reports whether dir names another live process as its owner.
func ownerAlive(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, ownerFile))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	ok, err := process.PidExists(int32(pid))
	return err == nil && ok
}
