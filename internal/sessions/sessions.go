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
			return []Session{}, nil
		}
		return nil, err
	}
	out := []Session{}
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
		title, err := readTitle(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out = append(out, Session{ID: id, Modified: info.ModTime(), Title: title})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
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
	f, err := os.Open(path)
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
		case l.Type == "ai-title" && l.AITitle != "":
			aiTitle = l.AITitle
		case l.Type == "user" && firstPrompt == "":
			var s string
			if json.Unmarshal(l.Message.Content, &s) == nil && s != "" && !strings.HasPrefix(s, "<") {
				firstPrompt = cut(s)
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
			if acc != nil {
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

func cut(s string) string {
	r := []rune(s)
	if len(r) > maxTitleRunes {
		r = r[:maxTitleRunes]
	}
	return string(r)
}
