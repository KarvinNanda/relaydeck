package cswap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/status.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStatusParse(t *testing.T) {
	with := fixture(t)
	info, err := parseStatus(with)
	if err != nil || info.DefaultAccount != "1" {
		t.Fatalf("with banner: %+v %v", info, err)
	}
	without := "Status: Account-3 (user1@example.com [org])\n  Total managed accounts: 3\n"
	info, err = parseStatus(without)
	if err != nil || info.DefaultAccount != "3" {
		t.Fatalf("without banner: %+v %v", info, err)
	}
}

func TestStatusUnknownFormat(t *testing.T) {
	for _, in := range []string{"", "Status: nothing here\n", "Not logged in\n"} {
		if _, err := parseStatus(in); !errors.Is(err, ErrUnknownFormat) {
			t.Fatalf("%q: err=%v", in, err)
		}
	}
}

func TestMapNoMappings(t *testing.T) {
	out := "No directory mappings yet.\nMap one with: cswap map <NUM|EMAIL> [PATH]\n"
	acc, ok := parseMap(out, "/work/a")
	if ok || acc != "" {
		t.Fatalf("got %q %v", acc, ok)
	}
}

func TestMapParsesClearLine(t *testing.T) {
	a := filepath.Join(os.TempDir(), "work", "a")
	ab := filepath.Join(os.TempDir(), "work", "ab")
	parent := filepath.Join(os.TempDir(), "work")
	out := "Directory mappings:\n  " + a + "  ->  Account-2 (user1@example.com)\n  " + ab + " -> Account-5\n"
	if acc, ok := parseMap(out, a); !ok || acc != "2" {
		t.Fatalf("got %q %v", acc, ok)
	}
	if acc, ok := parseMap(out, ab); !ok || acc != "5" {
		t.Fatalf("got %q %v", acc, ok)
	}
	if _, ok := parseMap(out, parent); ok {
		t.Fatal("prefix of a path must not match")
	}
	slash := "  " + filepath.ToSlash(a) + " -> user1@example.com\n"
	if acc, ok := parseMap(slash, a); !ok || acc != "user1@example.com" {
		t.Fatalf("got %q %v", acc, ok)
	}
	if acc, ok := parseMap("1) "+a+" -> Account-2\n", a); !ok || acc != "2" {
		t.Fatalf("list number must not win: %q %v", acc, ok)
	}
}

func TestMapSkipsUnclearLines(t *testing.T) {
	a := filepath.Join(os.TempDir(), "work", "a")
	for _, line := range []string{
		a + " is mapped somewhere\n",
		"1) " + a + "\n",
		a + " 5\n",
		"Account-2 maps nothing here\n",
	} {
		if acc, ok := parseMap(line, a); ok {
			t.Errorf("%q: got %q", line, acc)
		}
	}
}

func TestMapIgnoresCaseOnCaseInsensitiveOS(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case-insensitive only on darwin and windows")
	}
	a := filepath.Join(os.TempDir(), "work", "a")
	line := strings.ToUpper(a) + " -> Account-4\n"
	if acc, ok := parseMap(line, a); !ok || acc != "4" {
		t.Fatalf("got %q %v", acc, ok)
	}
}

func TestIndexFoldKeepsOffsets(t *testing.T) {
	if got := indexFold("Ä /X/y -> Z", "/x/Y", true); got != 3 {
		t.Fatalf("got %d", got)
	}
	if got := indexFold("/X/y", "/x/Y", false); got != -1 {
		t.Fatalf("got %d", got)
	}
}

func TestNoMutatingCommands(t *testing.T) {
	if len(statusArgv) != 1 || statusArgv[0] != "status" {
		t.Fatalf("status argv %v", statusArgv)
	}
	if len(mapArgv) != 1 || mapArgv[0] != "map" {
		t.Fatalf("map argv %v", mapArgv)
	}
}

func TestStatusRunsBinaryWithStatusOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script")
	}
	dir := t.TempDir()
	fix, _ := filepath.Abs("testdata/status.txt")
	script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = status ] || exit 9\ncat '" + fix + "'\n"
	bin := filepath.Join(dir, "cswap")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := Status(context.Background(), bin)
	if err != nil || info.DefaultAccount != "1" {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := Status(context.Background(), filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

// The default login's email is read from the Status line, so a folder that
// stores the email can be matched too.
func TestStatusParsesEmail(t *testing.T) {
	info, err := parseStatus(fixture(t))
	if err != nil || info.DefaultEmail != "user1@example.com" {
		t.Fatalf("with banner: %+v %v", info, err)
	}
	info, err = parseStatus("Status: Account-3 (user1@example.com [org])\n")
	if err != nil || info.DefaultAccount != "3" || info.DefaultEmail != "user1@example.com" {
		t.Fatalf("without banner: %+v %v", info, err)
	}
	// No email in the line: the number still parses, the email stays empty.
	info, err = parseStatus("Status: Account-4 (no email here)\n")
	if err != nil || info.DefaultAccount != "4" || info.DefaultEmail != "" {
		t.Fatalf("no email: %+v %v", info, err)
	}
}
