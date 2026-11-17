package daemon

import (
	"fmt"

	"github.com/KarvinNanda/relaydeck/internal/agent"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
	"github.com/KarvinNanda/relaydeck/internal/notify"
	"github.com/KarvinNanda/relaydeck/internal/sessions"
)

// State is a snapshot of everything the UI shows.
type State struct {
	Platform   string           `json:"platform"`
	Account    Account          `json:"account"`
	Workspaces []WorkspaceState `json:"workspaces"`
}

type WorkspaceState struct {
	Name    string        `json:"name"`
	Folders []FolderState `json:"folders"`
}

type FolderState struct {
	Name           string       `json:"name"`
	Path           string       `json:"path"`
	Account        string       `json:"account,omitempty"` // always "" on windows
	PermissionMode string       `json:"permission_mode"`
	SessionID      string       `json:"session_id,omitempty"`
	Status         agent.Status `json:"status"`
	// Screen is "trust" when the trust dialog is up, "waiting_for_prompt"
	// when no input box is visible, "" otherwise or when not running.
	Screen           string `json:"screen,omitempty"`
	OnDefaultLogin   bool   `json:"on_default_login"`  // a cswap switch would move this agent
	Pending          int    `json:"pending"`           // unread: queued, held or nudged
	AwaitingApproval int    `json:"awaiting_approval"` // held at the hop limit
}

// State returns a snapshot. The config is copied under cfgMu; agents and
// messages are read after the lock is released.
func (d *Daemon) State() State {
	d.cfgMu.Lock()
	wss := cloneWorkspaces(d.cfg.Workspaces)
	d.cfgMu.Unlock()

	acct := d.account()
	windows := d.opts.GOOS == "windows"
	st := State{Platform: d.opts.GOOS, Account: acct, Workspaces: make([]WorkspaceState, 0, len(wss))}
	for _, w := range wss {
		awaiting := map[string]int{}
		for _, m := range d.router.Timeline(w.Name) {
			if m.State == notify.StateAwaitingApproval {
				awaiting[m.To]++
			}
		}
		ws := WorkspaceState{Name: w.Name, Folders: make([]FolderState, 0, len(w.Folders))}
		for _, f := range w.Folders {
			fs := FolderState{
				Name:             f.Name,
				Path:             f.Path,
				Account:          f.Account,
				PermissionMode:   f.PermissionMode,
				SessionID:        f.SessionID,
				Status:           agent.StatusStopped,
				Pending:          len(d.router.Pending(w.Name, f.Name)),
				AwaitingApproval: awaiting[f.Name],
			}
			if fs.PermissionMode == "" {
				fs.PermissionMode = "auto"
			}
			if windows {
				fs.Account = ""
			}
			fs.OnDefaultLogin = onDefaultLogin(fs.Account, acct)
			if a := d.running(delivery.Target{WS: w.Name, Folder: f.Name}); a != nil {
				fs.Status = a.Status()
				switch scr := a.ScreenState(); {
				case scr.Trust:
					fs.Screen = "trust"
				case !scr.InputVisible:
					fs.Screen = "waiting_for_prompt"
				}
			}
			ws.Folders = append(ws.Folders, fs)
		}
		st.Workspaces = append(st.Workspaces, ws)
	}
	return st
}

// Sessions lists the Claude Code sessions of a folder, newest first.
func (d *Daemon) Sessions(ws, folder string) ([]sessions.Session, error) {
	_, f, err := d.folderSnapshot(ws, folder)
	if err != nil {
		return nil, err
	}
	return sessions.List(d.opts.ClaudeHome, f.Path)
}

// Messages returns the workspace timeline, oldest first.
func (d *Daemon) Messages(ws string) ([]notify.Message, error) {
	if !d.hasWorkspace(ws) {
		return nil, fmt.Errorf("%w: workspace %q", ErrNotFound, ws)
	}
	msgs := d.router.Timeline(ws)
	if msgs == nil {
		msgs = []notify.Message{}
	}
	return msgs, nil
}

// Approve releases a message held at the hop limit. The user asked for it
// to be delivered, so a user stop of the target no longer holds it.
func (d *Daemon) Approve(id string) error {
	if err := d.router.Approve(id); err != nil {
		return err
	}
	for _, t := range d.targets() {
		for _, m := range d.router.Pending(t.WS, t.Folder) {
			if m.ID == id {
				d.life.release(t)
			}
		}
	}
	d.deliv.Kick()
	return nil
}

// Reject drops a message held at the hop limit.
func (d *Daemon) Reject(id string) error { return d.router.Reject(id) }

// DeliverNow lets the next delivery to this folder skip the draft check once.
func (d *Daemon) DeliverNow(ws, folder string) error {
	if _, _, err := d.folderSnapshot(ws, folder); err != nil {
		return err
	}
	d.life.release(delivery.Target{WS: ws, Folder: folder})
	d.deliv.DeliverNow(ws, folder)
	return nil
}
