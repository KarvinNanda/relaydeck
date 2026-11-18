package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testToken = "tok-abc_123"

func TestWriteOpenPageModesAndContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relaydeck", "open.html")
	if err := writeOpenPage(path, 7341, testToken); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `url=http://127.0.0.1:7341/#token=`+testToken+`"`) {
		t.Fatalf("page lacks the url with port and token: %s", b)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %v, want 0600", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(path))
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %v, want 0700", di.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestRemoveOpenPageKeepsOtherToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open.html")
	if err := writeOpenPage(path, 7341, "newer-daemon-token"); err != nil {
		t.Fatal(err)
	}
	removeOpenPage(path, testToken)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("page of another token was removed: %v", err)
	}
	removeOpenPage(path, "newer-daemon-token")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("own page not removed: %v", err)
	}
}

func TestOpenMissingPage(t *testing.T) {
	var out, errb bytes.Buffer
	called := false
	code := runOpen(filepath.Join(t.TempDir(), "none.html"), &out, &errb, func(string) error { called = true; return nil })
	if code != 1 || called {
		t.Fatalf("code=%d called=%v", code, called)
	}
	if got := errb.String(); got != "relaydeck is not running. Start it with: relaydeck serve\n" {
		t.Fatalf("message = %q", got)
	}
}

func TestOpenStalePage(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	path := filepath.Join(t.TempDir(), "open.html")
	if err := writeOpenPage(path, port, testToken); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	called := false
	code := runOpen(path, &bytes.Buffer{}, &errb, func(string) error { called = true; return nil })
	if code != 1 || called {
		t.Fatalf("code=%d called=%v", code, called)
	}
	want := "relaydeck is not running on port " + itoa(port) + " (stale open page). Start it with: relaydeck serve\n"
	if errb.String() != want {
		t.Fatalf("message = %q, want %q", errb.String(), want)
	}
}

func TestOpenLiveCallsOpenerWithPathOnly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	path := filepath.Join(t.TempDir(), "open.html")
	if err := writeOpenPage(path, ln.Addr().(*net.TCPAddr).Port, testToken); err != nil {
		t.Fatal(err)
	}
	var got []string
	code := runOpen(path, &bytes.Buffer{}, &bytes.Buffer{}, func(p string) error {
		got = append(got, openerArgv(p)...)
		return nil
	})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(got) == 0 || got[len(got)-1] != path {
		t.Fatalf("opener argv = %v, want path %q last", got, path)
	}
	for _, a := range got {
		if strings.Contains(a, testToken) {
			t.Fatalf("token in argv: %v", got)
		}
	}
}
