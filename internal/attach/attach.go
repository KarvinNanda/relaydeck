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

// saveMu makes the total-size check and the write one step: two uploads at
// once cannot both see the same used size and pass the cap together (#72).
// Uploads are local and rare, so serializing them costs nothing noticeable.
var saveMu sync.Mutex

var exts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// Save writes one image from r into dir and returns its absolute path. dir is
// created (0700) inside its parent, which must exist: an agent that already
// exited has no runtime dir, and Save must not create it again. Neither the
// parent nor dir may be a symlink, and every file operation goes through an
// os.Root, so nothing escapes dir. The file is 0600 with a random name. On
// any error no file is left behind.
func Save(dir string, r io.Reader, maxFile, maxTotal int64) (string, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	head = head[:n]
	ext, ok := exts[http.DetectContentType(head)]
	if n == 0 || !ok {
		return "", ErrNotImage
	}
	parent, base := filepath.Dir(dir), filepath.Base(dir)
	if fi, err := os.Lstat(parent); err != nil {
		return "", err
	} else if !fi.IsDir() {
		return "", ErrUnsafeDir
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.Mkdir(base, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if fi, err := root.Lstat(base); err != nil {
		return "", err
	} else if !fi.IsDir() {
		return "", ErrUnsafeDir
	}
	sub, err := root.OpenRoot(base)
	if err != nil {
		return "", ErrUnsafeDir
	}
	defer sub.Close()

	saveMu.Lock()
	defer saveMu.Unlock()
	used, err := dirSize(sub)
	if err != nil {
		return "", err
	}
	name, err := randomName()
	if err != nil {
		return "", err
	}
	name += ext
	abs, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	f, err := sub.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	// One byte over the limit is read so a too large image is detected.
	written, err := io.Copy(f, io.MultiReader(bytes.NewReader(head), io.LimitReader(r, maxFile-int64(n)+1)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err == nil && written > maxFile:
		err = ErrTooLarge
	case err == nil && used+written > maxTotal:
		err = ErrFull
	}
	if err != nil {
		sub.Remove(name)
		return "", err
	}
	return abs, nil
}

func dirSize(r *os.Root) (int64, error) {
	es, err := fs.ReadDir(r.FS(), ".")
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, e := range es {
		if !e.Type().IsRegular() {
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
