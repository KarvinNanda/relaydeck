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
	"net/http"
	"os"
	"path/filepath"
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
)

var exts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// Save writes one image from r into dir and returns its absolute path. dir is
// created (0700) with os.Mkdir, so its parent must exist: an agent that
// already exited has no runtime dir, and Save must not create it again. The
// file is 0600 with a random name. On any error no file is left behind.
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
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	used, err := dirSize(dir)
	if err != nil {
		return "", err
	}
	name, err := randomName()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(dir, name+ext))
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
		os.Remove(abs)
		return "", err
	}
	return abs, nil
}

func dirSize(dir string) (int64, error) {
	es, err := os.ReadDir(dir)
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
