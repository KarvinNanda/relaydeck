package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const openDialTimeout = time.Second

// openPagePath is <user cache dir>/relaydeck/open.html.
func openPagePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "relaydeck", "open.html"), nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// openPageContent is ASCII only, with no script and no external resource.
// The token is in the URL fragment, which is never sent in a request.
func openPageContent(port int, token string) string {
	return `<!doctype html><meta charset="utf-8"><meta name="referrer" content="no-referrer">` +
		`<meta http-equiv="refresh" content="0;url=http://127.0.0.1:` + itoa(port) + `/#token=` + token + `">` +
		`<title>relaydeck</title><p>Opening relaydeck...</p>` + "\n"
}

// writeOpenPage writes the page to a 0600 temp file in a 0700 directory and
// renames it into place, so a reader never sees a partial file.
func writeOpenPage(path string, port int, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll keeps the mode of a directory that already exists.
	if err := os.Chmod(dir, 0o700); err != nil && runtime.GOOS != "windows" {
		return err
	}
	tmp, err := os.CreateTemp(dir, "open-*.tmp") // CreateTemp makes the file 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.WriteString(openPageContent(port, token)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// removeOpenPage deletes the page only when it still holds this run's token,
// so a newer daemon's page is never removed.
func removeOpenPage(path, token string) {
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "#token="+token+`"`) {
		return
	}
	os.Remove(path)
}

var openPagePort = regexp.MustCompile(`url=http://127\.0\.0\.1:(\d+)/`)

// openerArgv is the command that opens a file with the default application.
// The argument is a file path, never the URL, so the token stays out of argv.
func openerArgv(path string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open", path}
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", path}
	default:
		return []string{"xdg-open", path}
	}
}

func execOpener(path string) error {
	argv := openerArgv(path)
	return exec.Command(argv[0], argv[1:]...).Run()
}

// runOpen checks that a daemon answers on the port in the page, then opens
// the page file in the default browser.
func runOpen(path string, stdout, stderr io.Writer, opener func(string) error) int {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "relaydeck is not running. Start it with: relaydeck serve")
		} else {
			fmt.Fprintf(stderr, "relaydeck open: %v\n", err)
		}
		return 1
	}
	m := openPagePort.FindSubmatch(b)
	if m == nil {
		fmt.Fprintln(stderr, "relaydeck is not running. Start it with: relaydeck serve")
		return 1
	}
	port := string(m[1])
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), openDialTimeout)
	if err != nil {
		fmt.Fprintf(stderr, "relaydeck is not running on port %s (stale open page). Start it with: relaydeck serve\n", port)
		return 1
	}
	c.Close()
	if err := opener(path); err != nil {
		fmt.Fprintf(stderr, "relaydeck open: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "opened relaydeck in your browser")
	return 0
}
