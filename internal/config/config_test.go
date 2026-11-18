package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sample() *Config {
	return &Config{
		Port: 7341,
		Workspaces: []Workspace{{
			Name: "shop",
			Folders: []Folder{
				{Name: "fe-admin", Path: filepath.Join(t0(), "fe"), Account: "work", PermissionMode: "plan", SessionID: "abc"},
				{Name: "api", Path: filepath.Join(t0(), "api")},
			},
		}},
	}
}

// t0 returns an absolute base path that works on unix and windows.
func t0() string {
	if runtime.GOOS == "windows" {
		return `C:\work`
	}
	return "/work"
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 7341 || len(c.Workspaces) != 0 {
		t.Fatalf("got %+v", c)
	}
}

func TestLoadZeroPortMeansDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("workspaces: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 7341 {
		t.Fatalf("port = %d", c.Port)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("port: 7341\nworkspacs: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "workspacs") || !strings.Contains(err.Error(), p) {
		t.Fatalf("want error naming key and path, got %v", err)
	}
}

func TestLoadReportsInvalidConfigWithPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("port: 80\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("got %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.yaml")
	want := sample()
	if err := want.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != want.Port || len(got.Workspaces) != 1 || len(got.Workspaces[0].Folders) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got.Workspaces[0].Folders[0] != want.Workspaces[0].Folders[0] {
		t.Fatalf("folder differs: %+v", got.Workspaces[0].Folders[0])
	}
}

func TestSaveIsAtomicAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rd")
	p := filepath.Join(dir, "config.yaml")
	if err := sample().Save(p); err != nil {
		t.Fatal(err)
	}
	if err := sample().Save(p); err != nil { // overwrite path
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits only")
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	c := sample()
	c.Port = 80
	if err := c.Save(p); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file must not be written")
	}
}

func TestValidate(t *testing.T) {
	mut := func(f func(c *Config)) *Config { c := sample(); f(c); return c }
	tests := []struct {
		name    string
		c       *Config
		wantErr bool
	}{
		{"valid", sample(), false},
		{"dup workspace", mut(func(c *Config) { c.Workspaces = append(c.Workspaces, Workspace{Name: "shop"}) }), true},
		{"dup folder", mut(func(c *Config) {
			c.Workspaces[0].Folders = append(c.Workspaces[0].Folders, Folder{Name: "api", Path: t0()})
		}), true},
		{"same folder name in two workspaces", mut(func(c *Config) {
			c.Workspaces = append(c.Workspaces, Workspace{Name: "blog", Folders: []Folder{{Name: "api", Path: t0()}}})
		}), false},
		{"bad workspace name", mut(func(c *Config) { c.Workspaces[0].Name = "Shop" }), true},
		{"bad folder name", mut(func(c *Config) { c.Workspaces[0].Folders[0].Name = "Fe_Admin" }), true},
		{"name too long", mut(func(c *Config) { c.Workspaces[0].Name = strings.Repeat("a", 33) }), true},
		{"name 32 ok", mut(func(c *Config) { c.Workspaces[0].Name = strings.Repeat("a", 32) }), false},
		{"relative path", mut(func(c *Config) { c.Workspaces[0].Folders[0].Path = "rel/dir" }), true},
		{"empty path", mut(func(c *Config) { c.Workspaces[0].Folders[0].Path = "" }), true},
		{"mode yolo", mut(func(c *Config) { c.Workspaces[0].Folders[0].PermissionMode = "yolo" }), true},
		{"mode bypassPermissions", mut(func(c *Config) { c.Workspaces[0].Folders[0].PermissionMode = "bypassPermissions" }), false},
		{"mode empty", mut(func(c *Config) { c.Workspaces[0].Folders[0].PermissionMode = "" }), false},
		{"port 80", mut(func(c *Config) { c.Port = 80 }), true},
		{"port 1023", mut(func(c *Config) { c.Port = 1023 }), true},
		{"port 1024", mut(func(c *Config) { c.Port = 1024 }), false},
		{"port 65535", mut(func(c *Config) { c.Port = 65535 }), false},
		{"port 65536", mut(func(c *Config) { c.Port = 65536 }), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.c.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFolderReturnsPointersIntoConfig(t *testing.T) {
	c := sample()
	ws, f, ok := c.Folder("shop", "api")
	if !ok || ws.Name != "shop" || f.Name != "api" {
		t.Fatalf("lookup failed")
	}
	f.SessionID = "new"
	if c.Workspaces[0].Folders[1].SessionID != "new" {
		t.Fatal("not a pointer into config")
	}
	if _, _, ok := c.Folder("shop", "zzz"); ok {
		t.Fatal("unexpected hit")
	}
	if _, _, ok := c.Folder("zzz", "api"); ok {
		t.Fatal("unexpected hit")
	}
}

func TestDefaultPath(t *testing.T) {
	p, err := DefaultPath()
	if err != nil {
		t.Skip("no user config dir")
	}
	if !strings.HasSuffix(p, filepath.Join("relaydeck", "config.yaml")) {
		t.Fatalf("got %s", p)
	}
}

// A hand edit made between Load and a later change must survive that change.
func TestUpdateKeepsExternalEdit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := sample().Save(p); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	// The user adds a folder by hand while the daemon runs.
	edited := sample()
	edited.Workspaces[0].Folders = append(edited.Workspaces[0].Folders, Folder{Name: "hand", Path: filepath.Join(t0(), "hand")})
	if err := edited.Save(p); err != nil {
		t.Fatal(err)
	}
	// The daemon records a session id, as setSessionID does.
	err = c.Update(p, func(c *Config) error {
		_, f, ok := c.Folder("shop", "api")
		if !ok {
			t.Fatal("api missing after reload")
		}
		f.SessionID = "sid-1"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := got.Folder("shop", "hand"); !ok {
		t.Fatal("hand-added folder was lost")
	}
	if _, f, _ := got.Folder("shop", "api"); f.SessionID != "sid-1" {
		t.Fatalf("pending change not applied: %+v", f)
	}
	if _, _, ok := c.Folder("shop", "hand"); !ok {
		t.Fatal("live config did not pick up the hand edit")
	}
}

// Without an outside change, Update is a plain apply and save.
func TestUpdateWithoutExternalEdit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := sample().Save(p); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(p)
	for i := 0; i < 2; i++ { // the second call checks the hash after our own Save
		n := i
		if err := c.Update(p, func(c *Config) error {
			c.Workspaces = append(c.Workspaces, Workspace{Name: "w" + string(rune('a'+n))})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Load(p)
	if len(got.Workspaces) != 3 {
		t.Fatalf("workspaces = %d", len(got.Workspaces))
	}
}

func TestUpdateRefusesInvalidFreshFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := sample().Save(p); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(p)
	bad := []byte("port: [not yaml\n")
	if err := os.WriteFile(p, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	applied := false
	err := c.Update(p, func(*Config) error { applied = true; return nil })
	if err == nil || applied {
		t.Fatalf("err = %v, applied = %v, want an error and no apply", err, applied)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(bad) {
		t.Fatalf("invalid file was overwritten: %q", got)
	}
}

func TestUpdateErrorLeavesConfigUnchanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	c := sample()
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	err := c.Update(p, func(c *Config) error { c.Workspaces[0].Name = "x"; return os.ErrInvalid })
	if err == nil || c.Workspaces[0].Name != "shop" {
		t.Fatalf("err = %v, name = %q", err, c.Workspaces[0].Name)
	}
}

func TestSaveKeepsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "config.yaml")
	if err := sample().Save(real); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(link)
	c.Port = 7400
	if err := c.Save(link); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced by a regular file: %v %v", fi, err)
	}
	got, _ := Load(real)
	if got.Port != 7400 {
		t.Fatalf("target not updated: port %d", got.Port)
	}
}

func TestSaveWritesHeader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := sample().Save(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(data), "# Managed by relaydeck. Edits made while relaydeck runs are merged on the next save.\n") {
		t.Fatalf("no header: %q", data)
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDuplicateSessionAndSharedPath(t *testing.T) {
	c := sample()
	c.Workspaces = append(c.Workspaces, Workspace{Name: "blog", Folders: []Folder{
		{Name: "x", Path: filepath.Join(t0(), "other"), SessionID: "abc"},
	}})
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "session_id") {
		t.Fatalf("duplicate session_id across folders: %v", err)
	}
	// The same folder in two workspaces is allowed (spec 7.2). Only one
	// running agent per path is enforced, by agent.Manager.Start.
	c = sample()
	c.Workspaces = append(c.Workspaces, Workspace{Name: "blog", Folders: []Folder{
		{Name: "x", Path: filepath.Join(t0(), "api")},
	}})
	if err := c.Validate(); err != nil {
		t.Fatalf("same path in two workspaces must be allowed: %v", err)
	}
	c = sample()
	c.Workspaces[0].Folders[1].SessionID = "" // empty session ids never clash
	c.Workspaces[0].Folders[0].SessionID = ""
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
