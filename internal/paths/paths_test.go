package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProjectDirName(t *testing.T) {
	long := "/Users/karvin/relaydeck-spike/" + strings.Repeat("a", 120) + "/" + strings.Repeat("b", 120)
	sanitizedLong := "-Users-karvin-relaydeck-spike-" + strings.Repeat("a", 120) + "-" + strings.Repeat("b", 120)
	exact200 := "/" + strings.Repeat("a", 199)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"space", "/Users/karvin/relaydeck-spike/with space", "-Users-karvin-relaydeck-spike-with-space"},
		{"underscore and dot", "/Users/karvin/relaydeck-spike/my_app.v2", "-Users-karvin-relaydeck-spike-my-app-v2"},
		{"non-ascii letter", "/Users/karvin/relaydeck-spike/café", "-Users-karvin-relaydeck-spike-caf-"},
		{"emoji is two units", "/Users/karvin/relaydeck-spike/emoji🚀x", "-Users-karvin-relaydeck-spike-emoji--x"},
		{"plain", "/Users/karvin/KV/relaydeck", "-Users-karvin-KV-relaydeck"},
		{"windows style", `C:\Users\name\repo`, "C--Users-name-repo"},
		{"exactly 200 units is kept", exact200, "-" + strings.Repeat("a", 199)},
		{"271 chars gets hash suffix", long, sanitizedLong[:200] + "-opcytj"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProjectDirName(tt.in); got != tt.want {
				t.Errorf("ProjectDirName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCanonicalizeFixesCase(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case fixing applies to darwin and windows only")
	}
	base := realTemp(t)
	real := filepath.Join(base, "CaseTest")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "casetest")); err != nil {
		t.Skip("temp filesystem is case-sensitive")
	}
	got, err := Canonicalize(filepath.Join(base, "casetest"))
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Errorf("got %q, want %q", got, real)
	}
}

func TestCanonicalizeResolvesSymlink(t *testing.T) {
	base := realTemp(t)
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	got, err := Canonicalize(link)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("got %q, want %q", got, target)
	}
}

func TestCanonicalizeSpacesAndNonASCII(t *testing.T) {
	base := realTemp(t)
	for _, name := range []string{"with space", "café"} {
		dir := filepath.Join(base, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := Canonicalize(dir)
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if got != dir {
			t.Errorf("got %q, want %q", got, dir)
		}
	}
}

func TestCanonicalizeExpandsTilde(t *testing.T) {
	home := realTemp(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sub := filepath.Join(home, "proj")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Canonicalize("~/proj")
	if err != nil {
		t.Fatal(err)
	}
	if got != sub {
		t.Errorf("got %q, want %q", got, sub)
	}
}

func TestCanonicalizeRejectsFile(t *testing.T) {
	f := filepath.Join(realTemp(t), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Canonicalize(f); !errors.Is(err, ErrNotDir) {
		t.Errorf("err = %v, want ErrNotDir", err)
	}
}

func TestCanonicalizeRejectsMissing(t *testing.T) {
	if _, err := Canonicalize(filepath.Join(realTemp(t), "nope")); !errors.Is(err, ErrNotDir) {
		t.Errorf("err = %v, want ErrNotDir", err)
	}
}

func TestClaudeHome(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "/custom/claude")
		got, err := ClaudeHome()
		if err != nil || got != "/custom/claude" {
			t.Errorf("got %q, %v", got, err)
		}
	})
	t.Run("default", func(t *testing.T) {
		home := realTemp(t)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		got, err := ClaudeHome()
		if err != nil || got != filepath.Join(home, ".claude") {
			t.Errorf("got %q, %v", got, err)
		}
	})
}
