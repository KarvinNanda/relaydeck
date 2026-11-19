// Package attach saves an image the user dropped or pasted in a panel, so the
// agent's Claude Code can read it from a path. Only real images are kept: the
// type comes from the content (http.DetectContentType), never from a header
// or a name.
package attach

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

const (
	// MaxFile caps one image.
	MaxFile int64 = 10 << 20
	// MaxTotal caps all images in one attachments dir (one agent run).
	MaxTotal int64 = 256 << 20
)

var (
	ErrTooLarge = errors.New("image is larger than 10 MiB")
	ErrNotImage = errors.New("only PNG, JPEG, GIF and WebP images can be attached")
	ErrFull     = errors.New("attachment limit reached for this agent; restart it to clear")
	// ErrUnsafeDir: the runtime dir or the attachments dir is a symlink or
	// not a dir. An agent can write in its own runtime dir, so a link there
	// must never move an upload somewhere else (#72).
	ErrUnsafeDir = errors.New("attachment dir is not a plain directory")
)

// mu guards inflight: per attachments dir, the names of files still being
// written. The body is read without the lock, so a stalled upload never
// blocks another one. Only committed files count toward the total cap, and
// the check-and-commit step runs under the lock, so concurrent uploads cannot
// pass the cap together (#72).
var (
	mu       sync.Mutex
	inflight = map[string]map[string]bool{}
)

var exts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// Save writes one image from r into dir and returns its absolute path and
// size. dir is <runtime root>/<agent id>/attachments. Every file operation
// goes through an os.Root opened on the runtime root (owned by the daemon), so
// a symlink an agent plants or swaps in its runtime dir can never move the
// write outside that root (os.Root refuses links that escape it). The agent's
// runtime dir must exist: an agent that already exited has none, and Save
// does not create it again. The file is 0600 with a random name, created with
// O_EXCL. On any error no file is left behind.
func Save(dir string, r io.Reader, maxFile, maxTotal int64) (string, int64, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", 0, err
	}
	head = head[:n]
	ext, ok := exts[http.DetectContentType(head)]
	if n == 0 || !ok {
		return "", 0, ErrNotImage
	}
	sub, err := openDir(dir)
	if err != nil {
		return "", 0, err
	}
	defer sub.Close()
	name, err := randomName()
	if err != nil {
		return "", 0, err
	}
	name += ext
	abs, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", 0, err
	}

	mu.Lock()
	if inflight[dir] == nil {
		inflight[dir] = map[string]bool{}
	}
	inflight[dir][name] = true
	mu.Unlock()
	done := func() {
		delete(inflight[dir], name)
		if len(inflight[dir]) == 0 {
			delete(inflight, dir)
		}
	}

	written, err := write(sub, name, head, r, maxFile)
	mu.Lock()
	defer mu.Unlock()
	defer done()
	if err == nil {
		var used int64
		used, err = committedSize(sub, inflight[dir])
		if err == nil && used+written > maxTotal {
			err = ErrFull
		}
	}
	if err != nil {
		sub.Remove(name)
		return "", 0, err
	}
	return abs, written, nil
}

// openDir opens <root>/<id>/attachments through an os.Root on <root>,
// creating the attachments dir if needed. A missing runtime dir is reported
// as fs.ErrNotExist; a symlink or non-dir in the way as ErrUnsafeDir.
func openDir(dir string) (*os.Root, error) {
	idDir := filepath.Dir(dir)
	base, id, att := filepath.Dir(idDir), filepath.Base(idDir), filepath.Base(dir)
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	idRoot, err := openPlainDir(root, id)
	if err != nil {
		return nil, err
	}
	defer idRoot.Close()
	if err := idRoot.Mkdir(att, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return openPlainDir(idRoot, att)
}

// openPlainDir opens name inside r only if it is a real dir, not a symlink.
// The Lstat and the open are two steps, but a link swapped in between can
// only point inside the root: os.Root refuses one that escapes.
func openPlainDir(r *os.Root, name string) (*os.Root, error) {
	fi, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, ErrUnsafeDir
	}
	sub, err := r.OpenRoot(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, ErrUnsafeDir
	}
	return sub, nil
}

// write copies head and then at most maxFile bytes in total from r into a new
// file name; one byte over the limit is read, so a too large image is seen.
func write(sub *os.Root, name string, head []byte, r io.Reader, maxFile int64) (int64, error) {
	f, err := sub.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(f, io.MultiReader(bytes.NewReader(head), io.LimitReader(r, maxFile-int64(len(head))+1)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && written > maxFile {
		err = ErrTooLarge
	}
	return written, err
}

// committedSize sums the regular files in r, leaving out files still being
// written (skip).
func committedSize(r *os.Root, skip map[string]bool) (int64, error) {
	es, err := fs.ReadDir(r.FS(), ".")
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, e := range es {
		if !e.Type().IsRegular() || skip[e.Name()] {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue // removed meanwhile
		}
		sum += fi.Size()
	}
	return sum, nil
}

func randomName() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
