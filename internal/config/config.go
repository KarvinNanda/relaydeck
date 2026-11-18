// Package config holds the human-editable YAML registry of workspaces and folders.
package config

import (
	"bytes"
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
}

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
		return &Config{Port: DefaultPort}, nil
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
	return &c, nil
}

// Save validates, then writes atomically: temp file in the same dir (0600),
// fsync, rename over the target. The dir is created with 0700.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid config: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
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
	return nil
}

// Validate checks names, paths, permission modes and the port range.
func (c *Config) Validate() error {
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range 1024-65535", c.Port)
	}
	seenWS := map[string]bool{}
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
