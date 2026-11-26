package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/notify"
)

// waitConfigEvent waits for a "config" event, which makes every open tab
// reload its state.
func waitConfigEvent(t *testing.T, ch <-chan Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == "config" {
				return
			}
		case <-deadline:
			t.Fatal("no config event")
		}
	}
}

// #116: a stopped folder is removed from the config (a hand edit is kept),
// its waiting messages are rejected, and nothing can send to it any more.
// Only the config entry goes: the folder on disk is untouched.
func TestRemoveFolder(t *testing.T) {
	e := newEnv(t, nil)
	c, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c.Workspaces[0].Folders = append(c.Workspaces[0].Folders, config.Folder{Name: "hand", Path: e.dirC})
	if err := c.Save(e.cfgPath); err != nil {
		t.Fatal(err)
	}
	m, err := e.d.router.Send("w", "a", "b", "waiting")
	if err != nil {
		t.Fatal(err)
	}
	ev, cancel := e.d.Subscribe()
	defer cancel()

	if err := e.d.RemoveFolder("w", "b"); err != nil {
		t.Fatal(err)
	}
	waitConfigEvent(t, ev)

	saved, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range saved.Workspaces[0].Folders {
		names = append(names, f.Name)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "hand" {
		t.Fatalf("saved folders = %v, want [a hand]", names)
	}
	if _, _, ok := e.d.cfg.Folder("w", "b"); ok {
		t.Fatal("live config still has b")
	}
	for _, x := range e.d.router.Timeline("w") {
		if x.ID == m.ID && x.State != notify.StateRejected {
			t.Fatalf("waiting message is %s, want rejected", x.State)
		}
	}
	if _, err := e.d.router.Send("w", "a", "b", "again"); err == nil {
		t.Fatal("send to a removed folder was accepted")
	}
	if _, _, err := e.d.folderSnapshot("w", "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("start lookup after remove: %v, want ErrNotFound", err)
	}
	if err := e.d.RemoveFolder("w", "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove: %v, want ErrNotFound", err)
	}
	if err := e.d.RemoveFolder("nope", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown workspace: %v, want ErrNotFound", err)
	}
}

// A folder whose agent runs is refused, so a removed folder never has a
// live claude the UI cannot reach. After a stop it can be removed, and its
// stopped agent (scrollback) is dropped too.
func TestRemoveFolderRefusesRunning(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.d.Run(ctx)
	if err := e.d.StartAgent("w", "b", "", false); err != nil {
		t.Fatal(err)
	}
	if err := e.d.RemoveFolder("w", "b"); !errors.Is(err, ErrRemoveRunning) || err.Error() != "a running agent cannot be removed: stop b first" {
		t.Fatalf("remove of a running folder: %v, want ErrRemoveRunning", err)
	}
	if _, _, ok := e.d.cfg.Folder("w", "b"); !ok {
		t.Fatal("a refused remove changed the config")
	}
	id := e.agentID(t, "b")
	if err := e.d.StopAgent("w", "b", true); err != nil {
		t.Fatal(err)
	}
	tb := delivery.Target{WS: "w", Folder: "b"}
	eventually(t, "agent b stopped", func() bool { return e.d.running(tb) == nil })
	if err := e.d.RemoveFolder("w", "b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.d.Agent("w", "b"); ok {
		t.Fatal("the stopped agent of a removed folder is still listed")
	}
	if _, ok := e.d.mgr.Get(id); ok {
		t.Fatal("the stopped agent is still in the manager")
	}
	if e.d.isHeld(tb) {
		t.Fatal("the user-stop hold of a removed folder is kept")
	}
}

// A workspace goes with all its folders, but only when none of them runs.
func TestRemoveWorkspace(t *testing.T) {
	needPTY(t)
	e := newEnv(t, nil)
	if err := e.d.AddWorkspace("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.AddFolder("x", config.Folder{Name: "c", Path: e.dirC}); err != nil {
		t.Fatal(err)
	}
	// No Run: the delivery loop would start a for the waiting message.
	m, err := e.d.router.Send("w", "b", "a", "waiting")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.d.StartAgent("w", "b", "", false); err != nil {
		t.Fatal(err)
	}
	if err := e.d.RemoveWorkspace("w"); !errors.Is(err, ErrRemoveRunning) {
		t.Fatalf("remove with a running folder: %v, want ErrRemoveRunning", err)
	}
	if err := e.d.StopAgent("w", "b", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "agent b stopped", func() bool { return e.d.running(delivery.Target{WS: "w", Folder: "b"}) == nil })
	ev, cancelEv := e.d.Subscribe()
	defer cancelEv()
	if err := e.d.RemoveWorkspace("w"); err != nil {
		t.Fatal(err)
	}
	waitConfigEvent(t, ev)
	saved, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Workspaces) != 1 || saved.Workspaces[0].Name != "x" {
		t.Fatalf("saved workspaces = %+v, want only x", saved.Workspaces)
	}
	for _, x := range e.d.router.Timeline("w") {
		if x.ID == m.ID && x.State != notify.StateRejected {
			t.Fatalf("waiting message is %s, want rejected", x.State)
		}
	}
	if err := e.d.RemoveWorkspace("w"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove: %v, want ErrNotFound", err)
	}
}
