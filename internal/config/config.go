// Package config holds the human-editable YAML registry of workspaces and folders.
package config

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// DefaultPort is used when the file is missing or port is zero.
const DefaultPort = 7341

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

var permissionModes = map[string]bool{
	"": true, "acceptEdits": true, "auto": true, "bypassPermissions": true,
	"manual": true, "dontAsk": true, "plan": true,
}

type Config struct {
	Port       int         `yaml:"port"`
	Workspaces []Workspace `yaml:"workspaces"`

	// seen is the hash of the file content at the last Load or Save (a missing
	// file hashes as empty content). Nil means this Config never touched the
	// file, so Update has nothing to compare against.
	seen *[sha256.Size]byte
}

// header is the first line of every saved file. Comments are not kept: the
// file is rewritten from the in-memory config.
const header = "# Managed by relaydeck. Edits made while relaydeck runs are merged on the next save.\n"

type Workspace struct {
	Name    string   `yaml:"name"`
	Folders []Folder `yaml:"folders"`
}

type Folder struct {
	Name           string `yaml:"name"`
	Path           string `yaml:"path"`                      // canonical, set by the API layer
	Account        string `yaml:"account,omitempty"`         // cswap account, ignored on windows
	PermissionMode string `yaml:"permission_mode,omitempty"` // empty means "auto"
	SessionID      string `yaml:"session_id,omitempty"`      // current session
}

// DefaultPath returns os.UserConfigDir()/relaydeck/config.yaml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "relaydeck", "config.yaml"), nil
}

// Load reads and validates the file. A missing file gives the default config.
// Unknown keys are an error so a typo in the hand-edited file is reported.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Port: DefaultPort, seen: hashOf(nil)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	c.seen = hashOf(data)
	return &c, nil
}

func hashOf(data []byte) *[sha256.Size]byte {
	h := sha256.Sum256(data)
	return &h
}

// changedOnDisk reports whether the file differs from what this Config last
// loaded or saved. An unreadable file counts as changed, so Update reloads it
// and reports the error instead of overwriting it.
func (c *Config) changedOnDisk(path string) bool {
	if c.seen == nil {
		return false
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = nil, nil
	}
	return err != nil || *hashOf(data) != *c.seen
}

func (c *Config) clone() *Config {
	out := &Config{Port: c.Port, seen: c.seen, Workspaces: make([]Workspace, len(c.Workspaces))}
	for i, w := range c.Workspaces {
		out.Workspaces[i] = Workspace{Name: w.Name, Folders: append([]Folder(nil), w.Folders...)}
	}
	return out
}

// Update applies one pending change and saves. When the file changed since
// the last Load or Save (a hand edit while the daemon runs), it loads the
// fresh file first and applies the change on top of it, so the edit is not
// lost. If the fresh file is invalid, nothing is written and the error says
// so. On any error c is unchanged. On success c holds what was saved.
func (c *Config) Update(path string, apply func(*Config) error) error {
	work := c.clone()
	if c.changedOnDisk(path) {
		fresh, err := Load(path)
		if err != nil {
			return fmt.Errorf("config changed on disk and was not overwritten: %w", err)
		}
		work = fresh
	}
	if err := apply(work); err != nil {
		return err
	}
	if err := work.Save(path); err != nil {
		return err
	}
	*c = *work
	return nil
}

// Save validates, then writes atomically: temp file in the same dir (0600),
// fsync, rename over the target. The dir is created with 0700.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid config: %w", err)
	}
	body, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	data := append([]byte(header), body...)
	// Write through a symlink: rename over the link itself would replace it
	// with a regular file.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp") // CreateTemp uses mode 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	c.seen = hashOf(data)
	return nil
}

// ValidateWith validates c with its workspaces replaced by next.
func (c *Config) ValidateWith(next []Workspace) error {
	return (&Config{Port: c.Port, Workspaces: next}).Validate()
}

// Validate checks names, paths, permission modes and the port range.
func (c *Config) Validate() error {
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range 1024-65535", c.Port)
	}
	seenWS := map[string]bool{}
	seenPath := map[string]string{} // canonical path -> "ws/folder"
	seenSID := map[string]string{}  // session_id -> "ws/folder"
	for _, ws := range c.Workspaces {
		if !nameRe.MatchString(ws.Name) {
			return fmt.Errorf("workspace name %q must match %s", ws.Name, nameRe)
		}
		if seenWS[ws.Name] {
			return fmt.Errorf("duplicate workspace %q", ws.Name)
		}
		seenWS[ws.Name] = true
		seenF := map[string]bool{}
		for _, f := range ws.Folders {
			if !nameRe.MatchString(f.Name) {
				return fmt.Errorf("folder name %q in workspace %q must match %s", f.Name, ws.Name, nameRe)
			}
			if seenF[f.Name] {
				return fmt.Errorf("duplicate folder %q in workspace %q", f.Name, ws.Name)
			}
			seenF[f.Name] = true
			if !filepath.IsAbs(f.Path) {
				return fmt.Errorf("folder %s/%s: path %q must be absolute", ws.Name, f.Name, f.Path)
			}
			if !permissionModes[f.PermissionMode] {
				return fmt.Errorf("folder %s/%s: unknown permission_mode %q", ws.Name, f.Name, f.PermissionMode)
			}
			id := ws.Name + "/" + f.Name
			if other, dup := seenPath[f.Path]; dup {
				return fmt.Errorf("folder %s: path %q is already used by folder %s", id, f.Path, other)
			}
			seenPath[f.Path] = id
			if f.SessionID != "" {
				if other, dup := seenSID[f.SessionID]; dup {
					return fmt.Errorf("folder %s: session_id %q is already used by folder %s", id, f.SessionID, other)
				}
				seenSID[f.SessionID] = id
			}
		}
	}
	return nil
}

// Folder finds a folder. The returned pointers point into c, so changes
// (for example SessionID) show up in the next Save.
func (c *Config) Folder(ws, name string) (*Workspace, *Folder, bool) {
	for i := range c.Workspaces {
		if c.Workspaces[i].Name != ws {
			continue
		}
		for j := range c.Workspaces[i].Folders {
			if c.Workspaces[i].Folders[j].Name == name {
				return &c.Workspaces[i], &c.Workspaces[i].Folders[j], true
			}
		}
	}
	return nil, nil, false
}
