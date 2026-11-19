package attach

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Smallest headers that http.DetectContentType recognizes, padded with zeros.
func img(kind string, size int) []byte {
	var head []byte
	switch kind {
	case "png":
		head = []byte("\x89PNG\x0D\x0A\x1A\x0A")
	case "jpg":
		head = []byte("\xFF\xD8\xFF\xE0")
	case "gif":
		head = []byte("GIF89a")
	case "webp":
		head = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	}
	b := make([]byte, size)
	copy(b, head)
	return b
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestSaveImageTypes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	for kind, ext := range map[string]string{"png": ".png", "jpg": ".jpg", "gif": ".gif", "webp": ".webp"} {
		data := img(kind, 2048)
		p, _, err := Save(dir, bytes.NewReader(data), MaxFile, MaxTotal)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if filepath.Dir(p) != dir || !strings.HasSuffix(p, ext) || !filepath.IsAbs(p) {
			t.Fatalf("%s: path %q", kind, p)
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: content differs (%v)", kind, err)
		}
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s: file mode %v", kind, fi.Mode().Perm())
			}
			if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
				t.Fatalf("dir mode %v", fi.Mode().Perm())
			}
		}
	}
}

func TestSaveRefusesNonImages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	for _, body := range []string{`<svg xmlns="http://www.w3.org/2000/svg"></svg>`, "<html><body>x</body></html>", "just text", ""} {
		if _, _, err := Save(dir, strings.NewReader(body), MaxFile, MaxTotal); !errors.Is(err, ErrNotImage) {
			t.Fatalf("%q: err = %v, want ErrNotImage", body, err)
		}
	}
	if f := files(t, dir); len(f) != 0 {
		t.Fatalf("files left: %v", f)
	}
}

func TestSaveSizeLimit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	const max = 4096
	if _, _, err := Save(dir, bytes.NewReader(img("png", max)), max, MaxTotal); err != nil {
		t.Fatalf("exactly max: %v", err)
	}
	if _, _, err := Save(dir, bytes.NewReader(img("png", max+1)), max, MaxTotal); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("max+1: err = %v, want ErrTooLarge", err)
	}
	if f := files(t, dir); len(f) != 1 {
		t.Fatalf("files = %v, want only the first", f)
	}
}

func TestSaveTotalLimit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	for i := 0; i < 2; i++ {
		if _, _, err := Save(dir, bytes.NewReader(img("png", 1000)), MaxFile, 2500); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if _, _, err := Save(dir, bytes.NewReader(img("png", 1000)), MaxFile, 2500); !errors.Is(err, ErrFull) {
		t.Fatalf("third: err = %v, want ErrFull", err)
	}
	if f := files(t, dir); len(f) != 2 {
		t.Fatalf("files = %v, want the first two", f)
	}
}

func TestSaveNeedsParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "agent")
	dir := filepath.Join(parent, "attachments")
	if _, _, err := Save(dir, bytes.NewReader(img("png", 100)), MaxFile, MaxTotal); err == nil {
		t.Fatal("save without the parent dir succeeded")
	}
	if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parent was created: %v", err)
	}
}

func TestSaveNamesAreUnique(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, _, err := Save(dir, bytes.NewReader(img("png", 64)), MaxFile, MaxTotal)
		if err != nil {
			t.Fatal(err)
		}
		if seen[p] {
			t.Fatalf("duplicate name %s", p)
		}
		seen[p] = true
	}
}

// A symlinked attachments dir (an agent can create one in its runtime dir)
// is refused, and nothing is written where it points (#72).
func TestSaveRefusesSymlinkedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra rights on windows")
	}
	parent := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(parent, "attachments")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Save(filepath.Join(parent, "attachments"), bytes.NewReader(img("png", 100)), MaxFile, MaxTotal); !errors.Is(err, ErrUnsafeDir) {
		t.Fatalf("err = %v, want ErrUnsafeDir", err)
	}
	if f := files(t, outside); len(f) != 0 {
		t.Fatalf("wrote through the symlink: %v", f)
	}
}

// A runtime dir that is itself a symlink is refused too (#72).
func TestSaveRefusesSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra rights on windows")
	}
	base := t.TempDir()
	real := t.TempDir()
	link := filepath.Join(base, "agent")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Save(filepath.Join(link, "attachments"), bytes.NewReader(img("png", 100)), MaxFile, MaxTotal); !errors.Is(err, ErrUnsafeDir) {
		t.Fatalf("err = %v, want ErrUnsafeDir", err)
	}
	if f := files(t, real); len(f) != 0 {
		t.Fatalf("created in the symlink target: %v", f)
	}
}

// Concurrent saves never pass the total cap together (#72).
func TestSaveTotalLimitConcurrent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	const each, total = 1000, 5500
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := Save(dir, bytes.NewReader(img("png", each)), MaxFile, total); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := ok.Load(); got != 5 {
		t.Fatalf("%d saves succeeded, want exactly 5 under a %d byte cap", got, total)
	}
}

// A stalled upload (the client stops sending mid-body) must not block
// another upload to the same dir (#72 review).
func TestSaveStalledUploadDoesNotBlockOthers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent", "attachments")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	stalled := make(chan error, 1)
	go func() {
		_, _, err := Save(dir, pr, MaxFile, MaxTotal)
		stalled <- err
	}()
	if _, err := pw.Write(img("png", 600)); err != nil { // past the 512-byte sniff, then silence
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := Save(dir, bytes.NewReader(img("png", 100)), MaxFile, MaxTotal)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second save: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a stalled upload blocked another save")
	}
	pw.CloseWithError(errors.New("client went away"))
	if err := <-stalled; err == nil {
		t.Fatal("the broken upload succeeded")
	}
	if f := files(t, dir); len(f) != 1 {
		t.Fatalf("files = %v, want only the finished one", f)
	}
}
