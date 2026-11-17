package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/logx"
)

// Ruling 8: real launch inputs.
func TestDefaultOptions(t *testing.T) {
	o, err := DefaultOptions("/cfg.yaml", &config.Config{Port: 7341}, 7341, logx.New(&syncBuf{}))
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if o.RelaydeckExe != exe || !filepath.IsAbs(o.RelaydeckExe) {
		t.Fatalf("RelaydeckExe = %q, want resolved %q", o.RelaydeckExe, exe)
	}
	cache, _ := os.UserCacheDir()
	if o.RuntimeRoot != filepath.Join(cache, "relaydeck", "agents") {
		t.Fatalf("RuntimeRoot = %q", o.RuntimeRoot)
	}
	if len(o.BaseEnv) == 0 || o.ClaudeHome == "" {
		t.Fatalf("options missing defaults: %+v", o)
	}
	d, err := New(o)
	if err != nil {
		t.Fatalf("New(DefaultOptions) = %v", err)
	}
	if d.opts.ExternalOpen == nil || d.opts.OwnPIDs == nil || d.opts.CswapStatus == nil ||
		d.opts.StopGrace != 5*time.Second || d.opts.CswapEvery != 30*time.Second || d.opts.EnterDelay != 50*time.Millisecond {
		t.Fatalf("New did not fill defaults: %+v", d.opts)
	}
}

// Rulings 8 and 17: cswap is looked up on darwin and linux only.
func TestCswapPathPerPlatform(t *testing.T) {
	found := func(string) (string, error) { return "/usr/local/bin/cswap", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	for _, tc := range []struct {
		goos string
		look func(string) (string, error)
		want string
	}{
		{"darwin", found, "/usr/local/bin/cswap"},
		{"linux", found, "/usr/local/bin/cswap"},
		{"darwin", missing, ""},
		{"windows", found, ""},
		{"freebsd", found, ""}, // ruling 8: darwin and linux only
		{"", found, ""},
	} {
		if got := cswapPath(tc.goos, tc.look); got != tc.want {
			t.Errorf("cswapPath(%s) = %q, want %q", tc.goos, got, tc.want)
		}
	}
	// New also clears a cswap path on windows, so a hand-made Options cannot
	// turn on per-folder accounts there.
	e := newEnv(t, func(o *Options) { o.GOOS = "windows"; o.CswapPath = "/x/cswap" })
	if e.d.opts.CswapPath != "" || e.d.State().Account.Cswap {
		t.Fatal("cswap enabled on windows")
	}
	if !strings.HasPrefix(e.d.State().Platform, "windows") {
		t.Fatal("platform not windows")
	}
}
