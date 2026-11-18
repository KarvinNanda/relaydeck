package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

const (
	staleID  = "11111111-1111-4111-8111-111111111111"
	liveID   = "22222222-2222-4222-8222-222222222222"
	linkID   = "33333333-3333-4333-8333-333333333333"
	ownPIDID = "44444444-4444-4444-8444-444444444444"
)

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestCleanStaleRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agents")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk(staleID)
	os.WriteFile(filepath.Join(stale, "mcp.json"), []byte("{}"), 0o600)
	live := mk(liveID)
	if err := os.WriteFile(filepath.Join(live, ownerFile), []byte(strconv.Itoa(os.Getppid())), 0o600); err != nil {
		t.Fatal(err)
	}
	ownPID := mk(ownPIDID)
	os.WriteFile(filepath.Join(ownPID, ownerFile), []byte(strconv.Itoa(os.Getpid())), 0o600)
	other := mk("not-an-agent-id")
	file := filepath.Join(root, "55555555-5555-4555-8555-555555555555")
	os.WriteFile(file, []byte("x"), 0o600)
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(root, linkID)); err != nil {
			t.Fatal(err)
		}
	}

	n := CleanStaleRuntime(root, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if exists(stale) || exists(ownPID) {
		t.Fatal("stale dirs were not removed")
	}
	if n != 2 {
		t.Fatalf("removed = %d, want 2", n)
	}
	if !exists(live) {
		t.Fatal("dir of a live owner was removed")
	}
	if !exists(other) || !exists(file) {
		t.Fatal("a name that is not an agent id, or a file, was removed")
	}
	if runtime.GOOS != "windows" {
		if !exists(filepath.Join(root, linkID)) || !exists(filepath.Join(outside, "keep.txt")) {
			t.Fatal("a symlink was followed or removed")
		}
	}
}

// A runtime root that is a symlink is not followed.
func TestCleanStaleRuntimeIgnoresSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	target := t.TempDir()
	victim := filepath.Join(target, staleID)
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "agents")
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	if n := CleanStaleRuntime(root, slog.New(slog.NewTextHandler(io.Discard, nil))); n != 0 || !exists(victim) {
		t.Fatalf("followed a symlinked root: removed %d", n)
	}
}

func TestCleanStaleRuntimeMissingOrRelativeRoot(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if CleanStaleRuntime(filepath.Join(t.TempDir(), "none"), log) != 0 || CleanStaleRuntime("relative/agents", log) != 0 {
		t.Fatal("expected 0")
	}
}

// StartAgent marks its runtime dir with this process, so another relaydeck
// does not clean it.
func TestStartAgentWritesOwnerFile(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.StartAgent("w", "a", "", false); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(e.d.opts.RuntimeRoot, "*", ownerFile))
	if len(matches) != 1 {
		t.Fatalf("owner files = %v", matches)
	}
	b, _ := os.ReadFile(matches[0])
	if string(b) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("owner = %q", b)
	}
}
