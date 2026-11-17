package daemon

import (
	"fmt"
	"slices"

	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
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

// commit validates and saves next as the new workspace list, then swaps it
// in. On any error the live config is unchanged. Caller holds cfgMu.
func (d *Daemon) commit(next []config.Workspace) error {
	c := config.Config{Port: d.cfg.Port, Workspaces: next}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := c.Save(d.opts.ConfigPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	d.cfg.Workspaces = next
	return nil
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
	next := append(cloneWorkspaces(d.cfg.Workspaces), config.Workspace{Name: name})
	return d.commit(next)
}

// AddFolder adds a folder to ws and saves the config. The path is stored in
// canonical form and must be an existing directory. The account is dropped
// on windows. It returns the folder as stored.
func (d *Daemon) AddFolder(ws string, f config.Folder) (config.Folder, error) {
	p, err := paths.Canonicalize(f.Path)
	if err != nil {
		return config.Folder{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	f.Path = p
	if d.opts.GOOS == "windows" {
		f.Account = ""
	}

	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	next := cloneWorkspaces(d.cfg.Workspaces)
	i := slices.IndexFunc(next, func(w config.Workspace) bool { return w.Name == ws })
	if i < 0 {
		return config.Folder{}, fmt.Errorf("%w: workspace %q", ErrNotFound, ws)
	}
	if slices.ContainsFunc(next[i].Folders, func(o config.Folder) bool { return o.Name == f.Name }) {
		return config.Folder{}, fmt.Errorf("%w: folder %q in workspace %q", ErrConflict, f.Name, ws)
	}
	next[i].Folders = append(next[i].Folders, f)
	if err := d.commit(next); err != nil {
		return config.Folder{}, err
	}
	return f, nil
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
	f.SessionID = sid
	if err := d.cfg.Save(d.opts.ConfigPath); err != nil {
		d.log.Error("save config failed", "ws", ws, "folder", folder, "error", err.Error())
		return
	}
	d.log.Info("session id updated", "ws", ws, "folder", folder)
}
