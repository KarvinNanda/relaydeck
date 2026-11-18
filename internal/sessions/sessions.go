// Package sessions lists the Claude Code sessions of one folder. It only reads
// the session files under <claudeHome>/projects/<dir>/*.jsonl and never writes.
package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/paths"
)

const (
	// maxScan is how many bytes of one file are read to find a title. A real
	// ai-title line was seen near byte 380000 of a 650 KB file.
	maxScan = 8 << 20
	// maxTitleRunes is the cut length of a title taken from a user prompt.
	maxTitleRunes = 80
)

// openFile opens a session file. Tests replace it to count reads.
var openFile = os.Open

// titleCache remembers the parsed title of each file, so opening the picker
// does not re-read every file. An entry is valid while the file keeps the same
// mtime and size. Entries of a directory are pruned on each List of it, so the
// cache holds at most the files that still exist in listed directories.
var titleCache = struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}{m: map[string]cacheEntry{}}

type cacheEntry struct {
	mtime time.Time
	size  int64
	title string
}

// Session is one Claude Code session file of a folder.
type Session struct {
	ID       string // file name without .jsonl, always a UUID
	Modified time.Time
	Title    string // last ai-title, else first typed prompt cut to 80 runes, else ""
}

// List returns the sessions of canonicalDir, newest first. A missing project
// directory gives an empty slice and no error. A file that cannot be read is
// skipped.
func List(claudeHome, canonicalDir string) ([]Session, error) {
	dir := filepath.Join(claudeHome, "projects", paths.ProjectDirName(canonicalDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			pruneCache(dir, nil)
			return []Session{}, nil
		}
		return nil, err
	}
	out := []Session{}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		id, ok := strings.CutSuffix(name, ".jsonl")
		if !ok || e.IsDir() || !isUUID(id) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, name)
		title, err := cachedTitle(path, info)
		if err != nil {
			continue
		}
		seen[path] = true
		out = append(out, Session{ID: id, Modified: info.ModTime(), Title: title})
	}
	pruneCache(dir, seen)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// cachedTitle returns the title of path, parsing the file only when its mtime
// or size differs from the cached entry. The key comes from info, taken before
// the read, so a write during the read makes the next List parse again.
func cachedTitle(path string, info os.FileInfo) (string, error) {
	mtime, size := info.ModTime(), info.Size()
	titleCache.mu.Lock()
	c, ok := titleCache.m[path]
	titleCache.mu.Unlock()
	if ok && c.size == size && c.mtime.Equal(mtime) {
		return c.title, nil
	}
	title, err := readTitle(path)
	if err != nil {
		return "", err
	}
	titleCache.mu.Lock()
	titleCache.m[path] = cacheEntry{mtime: mtime, size: size, title: title}
	titleCache.mu.Unlock()
	return title, nil
}

// pruneCache drops the entries under dir whose path is not in keep.
func pruneCache(dir string, keep map[string]bool) {
	prefix := dir + string(filepath.Separator)
	titleCache.mu.Lock()
	defer titleCache.mu.Unlock()
	for p := range titleCache.m {
		if strings.HasPrefix(p, prefix) && !keep[p] {
			delete(titleCache.m, p)
		}
	}
}

// isUUID reports whether s has the 8-4-4-4-12 hex shape.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

var (
	markAITitle = []byte(`"type":"ai-title"`)
	markUser    = []byte(`"type":"user"`)
)

type line struct {
	Type    string `json:"type"`
	AITitle string `json:"aiTitle"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func readTitle(path string) (string, error) {
	f, err := openFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var aiTitle, firstPrompt string
	err = eachLine(io.LimitReader(f, maxScan), func(b []byte) {
		isAI := bytes.Contains(b, markAITitle)
		isUser := firstPrompt == "" && bytes.Contains(b, markUser)
		if !isAI && !isUser {
			return
		}
		var l line
		if json.Unmarshal(b, &l) != nil {
			return // corrupt line: ignore it, keep the session
		}
		switch {
		case l.Type == "ai-title" && oneLine(l.AITitle) != "":
			aiTitle = oneLine(l.AITitle)
		case l.Type == "user" && firstPrompt == "":
			var s string
			if json.Unmarshal(l.Message.Content, &s) == nil && s != "" && !strings.HasPrefix(s, "<") {
				firstPrompt = cut(oneLine(s))
			}
		}
	})
	if err != nil {
		return "", err
	}
	if aiTitle != "" {
		return aiTitle, nil
	}
	return firstPrompt, nil
}

// eachLine calls fn for every line of r. Lines longer than the reader buffer
// are joined from chunks. The caller caps the total read with io.LimitReader,
// so memory use per file is bounded by maxScan.
func eachLine(r io.Reader, fn func([]byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var acc []byte
	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case err == nil:
			if len(acc) > 0 {
				acc = append(acc, chunk...)
				fn(bytes.TrimRight(acc, "\r\n"))
				acc = acc[:0]
			} else {
				fn(bytes.TrimRight(chunk, "\r\n"))
			}
		case errors.Is(err, bufio.ErrBufferFull):
			acc = append(acc, chunk...)
		case errors.Is(err, io.EOF):
			// Last line without a newline. When the limit cut the file, this
			// may be a partial line: JSON decoding rejects it.
			acc = append(acc, chunk...)
			if len(acc) > 0 {
				fn(bytes.TrimRight(acc, "\r\n"))
			}
			return nil
		default:
			return err
		}
	}
}

// oneLine collapses every run of whitespace, newlines included, to one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func cut(s string) string {
	r := []rune(s)
	if len(r) > maxTitleRunes {
		r = r[:maxTitleRunes]
	}
	return string(r)
}
