package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/launch"
	"github.com/KarvinNanda/relaydeck/internal/paths"
)

// Every function here takes cfgMu for the whole read, change and Save.

// directory implements notify.Directory from the live config.
type directory struct{ d *Daemon }

// Folders lists the folder names of ws, or nil for an unknown workspace.
func (x directory) Folders(ws string) []string {
	x.d.cfgMu.Lock()
	defer x.d.cfgMu.Unlock()
	for _, w := range x.d.cfg.Workspaces {
		if w.Name == ws {
			names := make([]string, len(w.Folders))
			for i, f := range w.Folders {
				names[i] = f.Name
			}
			return names
		}
	}
	return nil
}

// targets lists every folder of every workspace, for the delivery loop.
func (d *Daemon) targets() []delivery.Target {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	var out []delivery.Target
	for _, w := range d.cfg.Workspaces {
		for _, f := range w.Folders {
			out = append(out, delivery.Target{WS: w.Name, Folder: f.Name})
		}
	}
	return out
}

// cloneWorkspaces deep-copies the workspace list, so a change can be
// validated and saved before it replaces the live one.
func cloneWorkspaces(ws []config.Workspace) []config.Workspace {
	out := make([]config.Workspace, len(ws))
	for i, w := range ws {
		out[i] = config.Workspace{Name: w.Name, Folders: slices.Clone(w.Folders)}
	}
	return out
}

// folderSnapshot returns copies of the workspace and folder.
func (d *Daemon) folderSnapshot(ws, folder string) (config.Workspace, config.Folder, error) {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	w, f, ok := d.cfg.Folder(ws, folder)
	if !ok {
		return config.Workspace{}, config.Folder{}, fmt.Errorf("%w: folder %s/%s", ErrNotFound, ws, folder)
	}
	return config.Workspace{Name: w.Name, Folders: slices.Clone(w.Folders)}, *f, nil
}

func (d *Daemon) hasWorkspace(ws string) bool {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	for _, w := range d.cfg.Workspaces {
		if w.Name == ws {
			return true
		}
	}
	return false
}

// commit builds the new workspace list with change, validates it and saves
// it, then swaps it in. If the file was edited by hand since the last save,
// change runs on the fresh file, so the edit is kept (config.Config.Update).
// On any error the live config is unchanged. Caller holds cfgMu.
func (d *Daemon) commit(change func(cur []config.Workspace) ([]config.Workspace, error)) error {
	var changeErr error
	err := d.cfg.Update(d.opts.ConfigPath, func(c *config.Config) error {
		next, err := change(cloneWorkspaces(c.Workspaces))
		if err == nil {
			if verr := c.ValidateWith(next); verr != nil {
				err = fmt.Errorf("%w: %v", ErrInvalid, verr)
			}
		}
		if err != nil {
			changeErr = err
			return err
		}
		c.Workspaces = next
		return nil
	})
	if err != nil && changeErr == nil {
		d.log.Error("save config failed", "error", err.Error())
		return fmt.Errorf("save config: %w", err)
	}
	if err == nil {
		// Every open tab reloads its state, not only the one that asked.
		d.hub.publish(Event{Type: "config"})
	}
	return err
}

// AddWorkspace adds an empty workspace and saves the config.
func (d *Daemon) AddWorkspace(name string) error {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	for _, w := range d.cfg.Workspaces {
		if w.Name == name {
			return fmt.Errorf("%w: workspace %q", ErrConflict, name)
		}
	}
	return d.commit(func(cur []config.Workspace) ([]config.Workspace, error) {
		if slices.ContainsFunc(cur, func(w config.Workspace) bool { return w.Name == name }) {
			return nil, fmt.Errorf("%w: workspace %q", ErrConflict, name)
		}
		return append(cur, config.Workspace{Name: name}), nil
	})
}

// AddFolder adds a folder to ws and saves the config. The path is stored in
// canonical form and must be an existing directory. The account must pass
// launch.ValidateAccount (so it is refused on windows). Without an account,
// the account cswap maps to the path is used, if any (spec 7.7). The cheap
// checks run first and `cswap map` last. It returns the folder as stored.
func (d *Daemon) AddFolder(ws string, f config.Folder) (config.Folder, error) {
	if err := launch.ValidateAccount(d.opts.GOOS, f.Account); err != nil {
		return config.Folder{}, err
	}
	p, err := paths.Canonicalize(f.Path)
	if err != nil {
		// The error holds the resolved path and raw file system text, so only
		// the log gets it. The caller sees fixed text.
		d.log.Warn("add folder: path refused", "ws", ws, "error", err.Error())
		return config.Folder{}, fmt.Errorf("%w: path must be an existing directory", ErrInvalid)
	}
	f.Path = p
	if err := d.checkNewFolder(ws, f); err != nil {
		return config.Folder{}, err
	}
	if f.Account == "" && d.opts.CswapPath != "" { // CswapPath is "" on windows
		f.Account = d.mappedAccount(p)
	}

	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	err = d.commit(func(cur []config.Workspace) ([]config.Workspace, error) {
		i, err := newFolderSlot(cur, ws, f.Name)
		if err != nil {
			return nil, err
		}
		cur[i].Folders = append(cur[i].Folders, f)
		return cur, nil
	})
	if err != nil {
		return config.Folder{}, err
	}
	return f, nil
}

// RemoveFolder removes a folder from the config (#116). Only the config
// entry goes: the directory and its Claude Code sessions stay on disk. A
// folder whose agent runs is refused (ErrRemoveRunning). runMu is held for
// writing, so no start can pass its config lookup while the check and the
// save run; a start after it gets ErrNotFound.
func (d *Daemon) RemoveFolder(ws, name string) error {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	t := delivery.Target{WS: ws, Folder: name}
	if d.running(t) != nil {
		return fmt.Errorf("%w: stop %s first", ErrRemoveRunning, name)
	}
	d.cfgMu.Lock()
	err := d.commit(func(cur []config.Workspace) ([]config.Workspace, error) {
		i := slices.IndexFunc(cur, func(w config.Workspace) bool { return w.Name == ws })
		if i < 0 {
			return nil, fmt.Errorf("%w: workspace %q", ErrNotFound, ws)
		}
		j := slices.IndexFunc(cur[i].Folders, func(f config.Folder) bool { return f.Name == name })
		if j < 0 {
			return nil, fmt.Errorf("%w: folder %s/%s", ErrNotFound, ws, name)
		}
		cur[i].Folders = slices.Delete(cur[i].Folders, j, j+1)
		return cur, nil
	})
	d.cfgMu.Unlock()
	if err != nil {
		return err
	}
	d.forgetFolder(t)
	d.log.Info("folder removed", "ws", ws, "folder", name)
	return nil
}

// RemoveWorkspace removes a workspace and all its folders from the config,
// as RemoveFolder does for one folder. It is refused while any of its
// folders has a running agent.
func (d *Daemon) RemoveWorkspace(ws string) error {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	var gone []string
	d.cfgMu.Lock()
	err := d.commit(func(cur []config.Workspace) ([]config.Workspace, error) {
		i := slices.IndexFunc(cur, func(w config.Workspace) bool { return w.Name == ws })
		if i < 0 {
			return nil, fmt.Errorf("%w: workspace %q", ErrNotFound, ws)
		}
		var busy []string
		gone = gone[:0]
		for _, f := range cur[i].Folders {
			gone = append(gone, f.Name)
			if d.running(delivery.Target{WS: ws, Folder: f.Name}) != nil {
				busy = append(busy, f.Name)
			}
		}
		if len(busy) > 0 {
			return nil, fmt.Errorf("%w: stop %s first", ErrRemoveRunning, strings.Join(busy, ", "))
		}
		return slices.Delete(cur, i, i+1), nil
	})
	d.cfgMu.Unlock()
	if err != nil {
		return err
	}
	for _, name := range gone {
		d.forgetFolder(delivery.Target{WS: ws, Folder: name})
	}
	d.log.Info("workspace removed", "ws", ws, "folders", len(gone))
	return nil
}

// forgetFolder drops what the daemon keeps for a folder whose config entry
// is gone: waiting messages (rejected, nothing can read them), its stopped
// agent with the scrollback, and a user-stop hold. Called without cfgMu:
// the router takes its own lock.
func (d *Daemon) forgetFolder(t delivery.Target) {
	if n := d.router.Drop(t.WS, t.Folder); n > 0 {
		d.log.Info("waiting messages rejected for a removed folder", "ws", t.WS, "folder", t.Folder, "count", n)
	}
	if id, ok := d.reg.dropLatest(t); ok {
		d.mgr.Remove(id)
		d.reg.forget(id)
	}
	d.life.release(t)
}

// checkNewFolder runs every check of AddFolder that does not need cswap
// (workspace, duplicate name, name, path, permission mode), before the slow
// `cswap map`. AddFolder checks the slot again under the lock, because the
// config can change while cswap runs.
func (d *Daemon) checkNewFolder(ws string, f config.Folder) error {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	next := cloneWorkspaces(d.cfg.Workspaces)
	i, err := newFolderSlot(next, ws, f.Name)
	if err != nil {
		return err
	}
	next[i].Folders = append(next[i].Folders, f)
	return d.validateWorkspaces(next)
}

// validateWorkspaces checks next as config.Validate does, as ErrInvalid.
// Caller holds cfgMu.
func (d *Daemon) validateWorkspaces(next []config.Workspace) error {
	c := config.Config{Port: d.cfg.Port, Workspaces: next}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// newFolderSlot returns the index of workspace ws, or ErrNotFound, or
// ErrConflict when it already has a folder called name.
func newFolderSlot(workspaces []config.Workspace, ws, name string) (int, error) {
	i := slices.IndexFunc(workspaces, func(w config.Workspace) bool { return w.Name == ws })
	if i < 0 {
		return 0, fmt.Errorf("%w: workspace %q", ErrNotFound, ws)
	}
	if slices.ContainsFunc(workspaces[i].Folders, func(o config.Folder) bool { return o.Name == name }) {
		return 0, fmt.Errorf("%w: folder %q in workspace %q", ErrConflict, name, ws)
	}
	return i, nil
}

// setSessionID stores sid as the folder's current session and saves the
// config when it changed. A save error is logged: the running agent is on
// that session either way, and the next save writes it.
func (d *Daemon) setSessionID(ws, folder, sid string) {
	if sid == "" {
		return
	}
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	_, f, ok := d.cfg.Folder(ws, folder)
	if !ok || f.SessionID == sid {
		return
	}
	// The change is re-applied on the fresh file when it was edited by hand.
	// A folder removed by hand in the meantime is simply skipped.
	err := d.cfg.Update(d.opts.ConfigPath, func(c *config.Config) error {
		if _, f, ok := c.Folder(ws, folder); ok {
			f.SessionID = sid
		}
		return nil
	})
	if err != nil {
		d.log.Error("save config failed", "ws", ws, "folder", folder, "error", err.Error())
		return
	}
	d.log.Info("session id updated", "ws", ws, "folder", folder)
}

// mappedAccount asks `cswap map` for the account of dir, outside cfgMu (it
// runs a command). An error gives no account; only the kind is logged, as
// cswap output can hold account emails.
func (d *Daemon) mappedAccount(dir string) string {
	acc, ok, err := d.opts.CswapMapped(context.Background(), d.opts.CswapPath, dir)
	if err != nil {
		d.log.Warn("cswap map failed, folder added without an account", "error", "command failed")
		return ""
	}
	if !ok {
		return ""
	}
	// The name goes into argv at start, so it gets the same check as one
	// typed by the user. Only the kind is logged, as with a cswap error.
	if err := launch.ValidateAccount(d.opts.GOOS, acc); err != nil {
		d.log.Warn("cswap map returned an unusable account, folder added without an account")
		return ""
	}
	return acc
}
