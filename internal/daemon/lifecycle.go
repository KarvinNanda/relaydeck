package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/paths"
)

// ErrNoTranscript: --resume needs the session file, or claude exits at once.
var ErrNoTranscript = errors.New("this session has no session file in ~/.claude/projects; pick another session or start a new one")

const (
	quickExitWindow   = 5 * time.Second  // an exit this soon after start is a failed start
	quickExitCooldown = 30 * time.Second // delivery does not restart that folder for this long
)

// lifecycle holds what delivery must respect about starting agents on its own.
type lifecycle struct {
	mu        sync.Mutex
	held      map[delivery.Target]time.Time // the user stopped it at this time
	quickExit map[delivery.Target]time.Time // its agent last exited right after start
	intended  map[string]bool               // agent ids stopped on purpose, not crashed
}

func newLifecycle() lifecycle {
	return lifecycle{
		held:      map[delivery.Target]time.Time{},
		quickExit: map[delivery.Target]time.Time{},
		intended:  map[string]bool{},
	}
}

// hold stops delivery from starting t for messages that are already waiting.
func (l *lifecycle) hold(t delivery.Target) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held[t] = time.Now()
}

func (l *lifecycle) release(t delivery.Target) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.held, t)
	delete(l.quickExit, t)
}

func (l *lifecycle) heldSince(t delivery.Target) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.held[t]
	return s, ok
}

func (l *lifecycle) markIntended(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.intended[id] = true
}

// exited records a crash on start: an exit within quickExitWindow that
// nobody asked for.
func (l *lifecycle) exited(t delivery.Target, id string, startedAt time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.intended[id] && time.Since(startedAt) < quickExitWindow {
		l.quickExit[t] = time.Now()
	}
	delete(l.intended, id)
}

func (l *lifecycle) quickExitRecently(t delivery.Target) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	q, ok := l.quickExit[t]
	return ok && time.Since(q) < quickExitCooldown
}

func (d *Daemon) isHeld(t delivery.Target) bool {
	_, ok := d.life.heldSince(t)
	return ok
}

// deliveryTargets is targets() without folders the user stopped. A message
// that arrives after the stop lifts the hold.
func (d *Daemon) deliveryTargets() []delivery.Target {
	all := d.targets()
	out := make([]delivery.Target, 0, len(all))
	for _, t := range all {
		if since, held := d.life.heldSince(t); held {
			if !d.messageSince(t, since) {
				continue
			}
			d.life.release(t)
		}
		out = append(out, t)
	}
	return out
}

func (d *Daemon) messageSince(t delivery.Target, since time.Time) bool {
	for _, m := range d.router.Pending(t.WS, t.Folder) {
		if m.Created.After(since) {
			return true
		}
	}
	return false
}

// hasTranscript reports whether Claude Code has a session file for id.
// relaydeck always runs cswap with --share-history, so it is in ClaudeHome.
func (d *Daemon) hasTranscript(dir, id string) bool {
	p := filepath.Join(d.opts.ClaudeHome, "projects", paths.ProjectDirName(dir), id+".jsonl")
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
