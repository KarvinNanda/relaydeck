package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	mustChtimes(t, filepath.Join(pd, idA+".jsonl"), now, now.Add(-3*time.Hour))
	mustChtimes(t, filepath.Join(pd, idB+".jsonl"), now, now.Add(-1*time.Hour))
	mustChtimes(t, filepath.Join(pd, idC+".jsonl"), now, now.Add(-2*time.Hour))
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
	ss := mustList(t, home, dir)
	if got := byID(t, ss, idA).Title; got != "fix the login bug" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleTruncatedTo80Runes(t *testing.T) {
	home, dir := setup(t, nil)
	long := strings.Repeat("é", 200)
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"user","message":{"role":"user","content":"`+long+`"}}`+"\n")
	ss := mustList(t, home, dir)
	if got := byID(t, ss, idA).Title; got != strings.Repeat("é", 80) {
		t.Fatalf("title has %d runes", len([]rune(got)))
	}
}

func TestCorruptLineSkipped(t *testing.T) {
	home, dir := setup(t, nil)
	writeRaw(t, home, dir, idA+".jsonl", "{\"type\":\"user\" broken\n"+`{"type":"user","message":{"role":"user","content":"after broken"}}`+"\n")
	ss := mustList(t, home, dir)
	if got := byID(t, ss, idA).Title; got != "after broken" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleFromAITitleLastWins(t *testing.T) {
	home, dir := setup(t, map[string]string{idB + ".jsonl": idB + ".jsonl"})
	ss := mustList(t, home, dir)
	if got := byID(t, ss, idB).Title; got != "Final title" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleSkipsCommandTags(t *testing.T) {
	home, dir := setup(t, map[string]string{idC + ".jsonl": idC + ".jsonl"})
	ss := mustList(t, home, dir)
	if got := byID(t, ss, idC).Title; got != "add a retry to the client" {
		t.Fatalf("title = %q", got)
	}
}

func TestLongLinesHandled(t *testing.T) {
	home, dir := setup(t, nil)
	pad := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 100*1024) + `"}}` + "\n"
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("y", 1024*1024) + `"}}` + "\n"
	writeRaw(t, home, dir, idA+".jsonl", pad+huge+`{"type":"ai-title","aiTitle":"After long lines"}`+"\n")
	ss := mustList(t, home, dir)
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
	ss := mustList(t, home, dir)
	if len(ss) != 1 || ss[0].ID != idA {
		t.Fatalf("got %+v", ss)
	}
}

func TestNoTitleIsEmpty(t *testing.T) {
	home, dir := setup(t, nil)
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"system"}`+"\n")
	ss := mustList(t, home, dir)
	if got := byID(t, ss, idA).Title; got != "" {
		t.Fatalf("title = %q", got)
	}
}

// countOpens replaces openFile for one test and returns a pointer to the
// number of files opened.
func countOpens(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := openFile
	openFile = func(p string) (*os.File, error) { n++; return os.Open(p) }
	t.Cleanup(func() { openFile = orig })
	return &n
}

func cacheHas(path string) bool {
	titleCache.mu.Lock()
	defer titleCache.mu.Unlock()
	_, ok := titleCache.m[path]
	return ok
}

func TestSecondListDoesNotReread(t *testing.T) {
	home, dir := setup(t, map[string]string{idA + ".jsonl": idA + ".jsonl", idB + ".jsonl": idB + ".jsonl"})
	opens := countOpens(t)
	first := mustList(t, home, dir)
	if *opens != 2 {
		t.Fatalf("first List opened %d files, want 2", *opens)
	}
	second := mustList(t, home, dir)
	if *opens != 2 {
		t.Fatalf("second List re-read files: %d opens", *opens)
	}
	if len(second) != 2 || byID(t, second, idB).Title != byID(t, first, idB).Title {
		t.Fatalf("cached result differs: %+v vs %+v", first, second)
	}
}

func TestChangedFileIsReparsed(t *testing.T) {
	home, dir := setup(t, nil)
	pd := filepath.Join(home, "projects", paths.ProjectDirName(dir))
	p := filepath.Join(pd, idA+".jsonl")
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"user","message":{"content":"one"}}`+"\n")
	opens := countOpens(t)
	ss := mustList(t, home, dir)
	if byID(t, ss, idA).Title != "one" {
		t.Fatalf("title = %q", ss[0].Title)
	}
	// Same size, new mtime.
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"user","message":{"content":"two"}}`+"\n")
	later := time.Now().Add(time.Hour)
	mustChtimes(t, p, later, later)
	ss = mustList(t, home, dir)
	if byID(t, ss, idA).Title != "two" || *opens != 2 {
		t.Fatalf("mtime change: title %q, opens %d", ss[0].Title, *opens)
	}
	// Same mtime, new size.
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"user","message":{"content":"three"}}`+"\n")
	mustChtimes(t, p, later, later)
	ss = mustList(t, home, dir)
	if byID(t, ss, idA).Title != "three" || *opens != 3 {
		t.Fatalf("size change: title %q, opens %d", ss[0].Title, *opens)
	}
}

func TestDeletedFileDropsFromResultAndCache(t *testing.T) {
	home, dir := setup(t, map[string]string{idA + ".jsonl": idA + ".jsonl", idB + ".jsonl": idB + ".jsonl"})
	pd := filepath.Join(home, "projects", paths.ProjectDirName(dir))
	pa, pb := filepath.Join(pd, idA+".jsonl"), filepath.Join(pd, idB+".jsonl")
	mustList(t, home, dir)
	if !cacheHas(pa) || !cacheHas(pb) {
		t.Fatal("entries not cached")
	}
	if err := os.Remove(pa); err != nil {
		t.Fatal(err)
	}
	ss := mustList(t, home, dir)
	if len(ss) != 1 || ss[0].ID != idB {
		t.Fatalf("got %+v", ss)
	}
	if cacheHas(pa) || !cacheHas(pb) {
		t.Fatalf("cache after delete: a=%v b=%v", cacheHas(pa), cacheHas(pb))
	}
}

func TestAITitleBeforeCapSurvivesInLargeFile(t *testing.T) {
	home, dir := setup(t, nil)
	pad := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 60*1024) + `"}}` + "\n"
	var b strings.Builder
	b.WriteString(`{"type":"ai-title","aiTitle":"Early title"}` + "\n")
	for b.Len() < maxScan+(1<<20) {
		b.WriteString(pad)
	}
	writeRaw(t, home, dir, idA+".jsonl", b.String())
	ss := mustList(t, home, dir)
	if got := byID(t, ss, idA).Title; got != "Early title" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleWhitespaceCollapsed(t *testing.T) {
	home, dir := setup(t, nil)
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"ai-title","aiTitle":"Line one\n  line\ttwo"}`+"\n")
	writeRaw(t, home, dir, idB+".jsonl", `{"type":"user","message":{"content":"first\n\nsecond   third"}}`+"\n")
	ss := mustList(t, home, dir)
	if got := byID(t, ss, idA).Title; got != "Line one line two" {
		t.Fatalf("ai title = %q", got)
	}
	if got := byID(t, ss, idB).Title; got != "first second third" {
		t.Fatalf("prompt title = %q", got)
	}
}

func TestEachLineAfterLongLineStillSplits(t *testing.T) {
	long := strings.Repeat("a", 100*1024)
	var got []int
	eachLine(strings.NewReader(long+"\nshort\nlast"), func(b []byte) { got = append(got, len(b)) })
	if len(got) != 3 || got[0] != len(long) || got[1] != 5 || got[2] != 4 {
		t.Fatalf("line lengths = %v", got)
	}
}

func mustList(t *testing.T, home, dir string) []Session {
	t.Helper()
	ss, err := List(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

func mustChtimes(t *testing.T, p string, at, mt time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, mt); err != nil {
		t.Fatal(err)
	}
}

func TestMissingProjectDirPrunesCache(t *testing.T) {
	home, dir := setup(t, map[string]string{idA + ".jsonl": idA + ".jsonl"})
	pd := filepath.Join(home, "projects", paths.ProjectDirName(dir))
	p := filepath.Join(pd, idA+".jsonl")
	mustList(t, home, dir)
	if !cacheHas(p) {
		t.Fatal("entry not cached")
	}
	if err := os.RemoveAll(pd); err != nil {
		t.Fatal(err)
	}
	if ss := mustList(t, home, dir); len(ss) != 0 {
		t.Fatalf("got %+v", ss)
	}
	if cacheHas(p) {
		t.Fatal("entry of a deleted project dir stayed in the cache")
	}
}

func TestWhitespaceAITitleKeepsEarlierTitle(t *testing.T) {
	home, dir := setup(t, nil)
	writeRaw(t, home, dir, idA+".jsonl", `{"type":"ai-title","aiTitle":"Real title"}`+"\n"+`{"type":"ai-title","aiTitle":"  \n "}`+"\n")
	if got := byID(t, mustList(t, home, dir), idA).Title; got != "Real title" {
		t.Fatalf("title = %q", got)
	}
}

func TestConcurrentListWithDelete(t *testing.T) {
	home, dir := setup(t, map[string]string{idA + ".jsonl": idA + ".jsonl", idB + ".jsonl": idB + ".jsonl", idC + ".jsonl": idC + ".jsonl"})
	pd := filepath.Join(home, "projects", paths.ProjectDirName(dir))
	var wg sync.WaitGroup
	errs := make(chan error, 9)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if _, err := List(home, dir); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := os.Remove(filepath.Join(pd, idA+".jsonl")); err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	ss := mustList(t, home, dir)
	if len(ss) != 2 || cacheHas(filepath.Join(pd, idA+".jsonl")) {
		t.Fatalf("after delete: %+v", ss)
	}
}
