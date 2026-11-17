package cswap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	out := "Directory mappings:\n  /work/a  ->  Account-2 (user1@example.com)\n  /work/ab -> Account-5\n"
	acc, ok := parseMap(out, "/work/a")
	if !ok || acc != "2" {
		t.Fatalf("got %q %v", acc, ok)
	}
	acc, ok = parseMap(out, "/work/ab")
	if !ok || acc != "5" {
		t.Fatalf("got %q %v", acc, ok)
	}
	if _, ok := parseMap(out, "/work"); ok {
		t.Fatal("prefix of a path must not match")
	}
	if _, ok := parseMap("/work/a is mapped somewhere\n", "/work/a"); ok {
		t.Fatal("a line with no account must be skipped")
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
