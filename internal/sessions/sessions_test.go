package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/paths"
)

const (
	idA = "11111111-1111-4111-8111-111111111111"
	idB = "22222222-2222-4222-8222-222222222222"
	idC = "33333333-3333-4333-8333-333333333333"
)

// setup copies fixtures into <home>/projects/<dirname>/ and returns home and dir.
func setup(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	home := t.TempDir()
	dir := "/work/demo"
	pd := filepath.Join(home, "projects", paths.ProjectDirName(dir))
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		data, err := os.ReadFile(filepath.Join("testdata", src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pd, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, dir
}

func writeRaw(t *testing.T, home, dir, name, content string) {
	t.Helper()
	pd := filepath.Join(home, "projects", paths.ProjectDirName(dir))
	if err := os.WriteFile(filepath.Join(pd, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byID(t *testing.T, ss []Session, id string) Session {
	t.Helper()
	for _, s := range ss {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("session %s not found in %v", id, ss)
	return Session{}
}

func TestListNewestFirst(t *testing.T) {
	home, dir := setup(t, map[string]string{idA + ".jsonl": idA + ".jsonl", idB + ".jsonl": idB + ".jsonl", idC + ".jsonl": idC + ".jsonl"})
	pd := filepath.Join(home, "projects", paths.ProjectDirName(dir))
	now := time.Now()
	os.Chtimes(filepath.Join(pd, idA+".jsonl"), now, now.Add(-3*time.Hour))
	os.Chtimes(filepath.Join(pd, idB+".jsonl"), now, now.Add(-1*time.Hour))
	os.Chtimes(filepath.Join(pd, idC+".jsonl"), now, now.Add(-2*time.Hour))
	ss, err := List(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 3 || ss[0].ID != idB || ss[1].ID != idC || ss[2].ID != idA {
		t.Fatalf("bad order: %+v", ss)
	}
}

func TestListMissingDirEmpty(t *testing.T) {
	ss, err := List(t.TempDir(), "/nope")
	if err != nil || ss == nil || len(ss) != 0 {
		t.Fatalf("want empty non-nil slice and nil error, got %v %v", ss, err)
	}
}

func TestTitleFromFirstUserPrompt(t *testing.T) {
	home, dir := setup(t, map[string]string{idA + ".jsonl": idA + ".jsonl"})
	ss, _ := List(home, dir)
	if got := byID(t, ss, idA).Title; got != "fix the login bug" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleTruncatedTo80Runes(t *testing.T) {
	home, dir := setup(t, nil)
	long := strings.Repeat("é", 200)
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"user","message":{"role":"user","content":"`+long+`"}}`+"\n")
	ss, _ := List(home, dir)
	if got := byID(t, ss, idA).Title; got != strings.Repeat("é", 80) {
		t.Fatalf("title has %d runes", len([]rune(got)))
	}
}

func TestCorruptLineSkipped(t *testing.T) {
	home, dir := setup(t, nil)
	writeRaw(t, home, dir, idA+".jsonl", "{\"type\":\"user\" broken\n"+`{"type":"user","message":{"role":"user","content":"after broken"}}`+"\n")
	ss, _ := List(home, dir)
	if got := byID(t, ss, idA).Title; got != "after broken" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleFromAITitleLastWins(t *testing.T) {
	home, dir := setup(t, map[string]string{idB + ".jsonl": idB + ".jsonl"})
	ss, _ := List(home, dir)
	if got := byID(t, ss, idB).Title; got != "Final title" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleSkipsCommandTags(t *testing.T) {
	home, dir := setup(t, map[string]string{idC + ".jsonl": idC + ".jsonl"})
	ss, _ := List(home, dir)
	if got := byID(t, ss, idC).Title; got != "add a retry to the client" {
		t.Fatalf("title = %q", got)
	}
}

func TestLongLinesHandled(t *testing.T) {
	home, dir := setup(t, nil)
	pad := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 100*1024) + `"}}` + "\n"
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("y", 1024*1024) + `"}}` + "\n"
	writeRaw(t, home, dir, idA+".jsonl", pad+huge+`{"type":"ai-title","aiTitle":"After long lines"}`+"\n")
	ss, _ := List(home, dir)
	if got := byID(t, ss, idA).Title; got != "After long lines" {
		t.Fatalf("title = %q", got)
	}
}

func TestOversizeFileNotFatal(t *testing.T) {
	home, dir := setup(t, nil)
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("y", 9*1024*1024) + `"}}` + "\n"
	writeRaw(t, home, dir, idA+".jsonl", huge)
	ss, err := List(home, dir)
	if err != nil || len(ss) != 1 || ss[0].Title != "" {
		t.Fatalf("got %+v %v", ss, err)
	}
}

func TestNonUUIDFilesSkipped(t *testing.T) {
	home, dir := setup(t, map[string]string{idA + ".jsonl": idA + ".jsonl", "notes.jsonl": "notes.jsonl", "README.txt": "notes.jsonl"})
	ss, _ := List(home, dir)
	if len(ss) != 1 || ss[0].ID != idA {
		t.Fatalf("got %+v", ss)
	}
}

func TestNoTitleIsEmpty(t *testing.T) {
	home, dir := setup(t, nil)
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"system"}`+"\n")
	ss, _ := List(home, dir)
	if got := byID(t, ss, idA).Title; got != "" {
		t.Fatalf("title = %q", got)
	}
}
