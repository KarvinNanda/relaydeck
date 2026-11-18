package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/KarvinNanda/relaydeck/internal/config"
)

// Spec 7.7: a folder added without an account gets the account cswap maps to
// its path. An account the user gave wins, a cswap error leaves it empty,
// and without cswap (always on windows) cswap is never asked.
func TestAddFolderPrefillsAccountFromCswap(t *testing.T) {
	type call struct{ path, dir string }
	var mu sync.Mutex
	var calls []call
	mapped := func(_ context.Context, path, dir string) (string, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{path, dir})
		return "3", true, nil
	}

	e := newEnv(t, func(o *Options) {
		o.GOOS = "linux"
		o.CswapPath = "/opt/cswap"
		o.CswapMapped = mapped
	})
	f, err := e.d.AddFolder("w", config.Folder{Name: "c", Path: e.dirC})
	if err != nil {
		t.Fatal(err)
	}
	if f.Account != "3" || e.savedFolder(t, "c").Account != "3" {
		t.Fatalf("account = %q (saved %q), want the mapped 3", f.Account, e.savedFolder(t, "c").Account)
	}
	if len(calls) != 1 || calls[0] != (call{"/opt/cswap", e.dirC}) {
		t.Fatalf("cswap map calls = %+v, want one with the canonical path", calls)
	}

	// The user's own account is kept and cswap is not asked.
	f, err = e.d.AddFolder("w", config.Folder{Name: "d", Path: e.dirD, Account: "1"})
	if err != nil || f.Account != "1" {
		t.Fatalf("explicit account: %+v %v", f, err)
	}
	if len(calls) != 1 {
		t.Fatalf("cswap asked although the account was given: %+v", calls)
	}

	// Nothing mapped, or an error: no account, and the add still works.
	e2 := newEnv(t, func(o *Options) {
		o.GOOS = "darwin"
		o.CswapPath = "/opt/cswap"
		o.CswapMapped = func(context.Context, string, string) (string, bool, error) {
			return "", false, errors.New("cswap failed: user@example.com")
		}
	})
	f, err = e2.d.AddFolder("w", config.Folder{Name: "c", Path: e2.dirC})
	if err != nil || f.Account != "" {
		t.Fatalf("cswap error: %+v %v", f, err)
	}
	if logs := e2.logs.String(); !strings.Contains(logs, "cswap map failed") || strings.Contains(logs, "user@example.com") {
		t.Fatalf("cswap error not logged, or logged with its text: %s", logs)
	}

	// Windows: no cswap, so it is never asked.
	asked := false
	e3 := newEnv(t, func(o *Options) {
		o.GOOS = "windows"
		o.CswapPath = "/opt/cswap"
		o.CswapMapped = func(context.Context, string, string) (string, bool, error) {
			asked = true
			return "3", true, nil
		}
	})
	if f, err = e3.d.AddFolder("w", config.Folder{Name: "c", Path: e3.dirC}); err != nil || f.Account != "" || asked {
		t.Fatalf("windows: %+v %v asked=%v", f, err, asked)
	}
}

// A hand edit made while the daemon runs must survive the session id save
// (the most frequent write), and an invalid hand edit must not be overwritten.
func TestSetSessionIDKeepsHandEdit(t *testing.T) {
	e := newEnv(t, nil)
	const sid = "22222222-2222-4222-8222-222222222222"

	c, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c.Workspaces[0].Folders = append(c.Workspaces[0].Folders, config.Folder{Name: "hand", Path: e.dirC})
	if err := c.Save(e.cfgPath); err != nil {
		t.Fatal(err)
	}

	e.d.setSessionID("w", "a", sid)

	if got := e.savedFolder(t, "hand"); got.Path != e.dirC {
		t.Fatalf("hand-added folder lost: %+v", got)
	}
	if got := e.savedFolder(t, "a").SessionID; got != sid {
		t.Fatalf("session id = %q, want %q", got, sid)
	}
}

func TestSetSessionIDDoesNotOverwriteInvalidFile(t *testing.T) {
	e := newEnv(t, nil)
	bad := []byte("port: [broken\n")
	if err := os.WriteFile(e.cfgPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	e.d.setSessionID("w", "a", "22222222-2222-4222-8222-222222222222")
	got, _ := os.ReadFile(e.cfgPath)
	if string(got) != string(bad) {
		t.Fatalf("invalid file overwritten: %q", got)
	}
	if !strings.Contains(e.logs.String(), "save config failed") {
		t.Fatalf("failure not logged: %s", e.logs.String())
	}
}
