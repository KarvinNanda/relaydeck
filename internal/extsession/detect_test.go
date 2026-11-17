package extsession

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func agents(ss ...Session) func(context.Context) ([]Session, error) {
	return func(context.Context) ([]Session, error) { return ss, nil }
}

func TestOpenMatchesSessionID(t *testing.T) {
	d := Detector{Agents: agents(Session{PID: 10, Cwd: "/other", SessionID: "s1", Kind: "interactive"})}
	pid, open, err := d.Open(context.Background(), "/work/a", "s1", nil)
	if err != nil || !open || pid != 10 {
		t.Fatalf("got pid=%d open=%v err=%v", pid, open, err)
	}
}

func TestOpenMatchesSameCanonicalCwd(t *testing.T) {
	d := Detector{Agents: agents(Session{PID: 11, Cwd: "/work/a/", SessionID: "zz", Kind: "interactive"})}
	pid, open, err := d.Open(context.Background(), "/work/a", "s1", nil)
	if err != nil || !open || pid != 11 {
		t.Fatalf("got pid=%d open=%v err=%v", pid, open, err)
	}
}

func TestOpenIgnoresNonInteractive(t *testing.T) {
	d := Detector{Agents: agents(Session{PID: 12, Cwd: "/work/a", SessionID: "s1", Kind: "background"})}
	_, open, err := d.Open(context.Background(), "/work/a", "s1", nil)
	if err != nil || open {
		t.Fatalf("open=%v err=%v", open, err)
	}
}

func TestOpenIgnoresExcludedPIDs(t *testing.T) {
	d := Detector{Agents: agents(Session{PID: 13, Cwd: "/work/a", SessionID: "s1", Kind: "interactive"})}
	_, open, err := d.Open(context.Background(), "/work/a", "s1", map[int]bool{13: true})
	if err != nil || open {
		t.Fatalf("open=%v err=%v", open, err)
	}
}

func TestOpenIgnoresOtherDirs(t *testing.T) {
	d := Detector{Agents: agents(Session{PID: 14, Cwd: "/work/ab", SessionID: "x", Kind: "interactive"})}
	_, open, err := d.Open(context.Background(), "/work/a", "s1", nil)
	if err != nil || open {
		t.Fatalf("open=%v err=%v", open, err)
	}
}

func TestEmptySessionIDNeverMatchesEmptyID(t *testing.T) {
	d := Detector{Agents: agents(Session{PID: 15, Cwd: "/other", SessionID: "", Kind: "interactive"})}
	_, open, _ := d.Open(context.Background(), "/work/a", "", nil)
	if open {
		t.Fatal("empty session ids must not match")
	}
}

func TestFallbackToProcs(t *testing.T) {
	d := Detector{
		Agents: func(context.Context) ([]Session, error) { return nil, errors.New("agents broke") },
		Procs: func() ([]Session, error) {
			return []Session{{PID: 20, Cwd: "/work/a", Kind: "interactive"}}, nil
		},
	}
	pid, open, err := d.Open(context.Background(), "/work/a", "s1", nil)
	if err != nil || !open || pid != 20 {
		t.Fatalf("got pid=%d open=%v err=%v", pid, open, err)
	}
}

func TestBothFailReturnsError(t *testing.T) {
	d := Detector{
		Agents: func(context.Context) ([]Session, error) { return nil, errors.New("agents broke") },
		Procs:  func() ([]Session, error) { return nil, errors.New("procs broke") },
	}
	_, _, err := d.Open(context.Background(), "/work/a", "s1", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseAgents(t *testing.T) {
	in := []byte(`[
 {"kind":"interactive","status":"idle","cwd":"/Users/x/repo","pid":74162,"sessionId":"11111111-2222-3333-4444-555555555555","name":"n","startedAt":"2026-11-18T00:00:00Z"},
 {"kind":"background","status":"busy","cwd":"/Users/x/other","pid":5,"sessionId":"abc"}
]`)
	got, err := ParseAgents(in)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v err=%v", got, err)
	}
	want := Session{PID: 74162, Cwd: "/Users/x/repo", SessionID: "11111111-2222-3333-4444-555555555555", Kind: "interactive"}
	if got[0] != want {
		t.Fatalf("got %+v want %+v", got[0], want)
	}
	if got[1].Kind != "background" {
		t.Fatalf("kind: %+v", got[1])
	}
	if _, err := ParseAgents([]byte("not json")); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestNoUserInputInArgv(t *testing.T) {
	if len(agentsArgv) != 2 || agentsArgv[0] != "agents" || agentsArgv[1] != "--json" {
		t.Fatalf("unexpected argv %v", agentsArgv)
	}
}

func TestProcsFindsClaudeNamedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	prog := `package main
import "time"
func main() { time.Sleep(time.Minute) }
`
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module tiny\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(bin, "claude")
	build := exec.Command("go", "build", "-o", claude, ".")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(claude)
	cmd.Dir = work
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	d := Detector{Procs: ListProcs}
	var pid int
	var open bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pid, open, err = d.Open(context.Background(), work, "", nil)
		if err == nil && open {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || !open || pid != cmd.Process.Pid {
		t.Fatalf("pid=%d open=%v err=%v want pid %d", pid, open, err, cmd.Process.Pid)
	}
	_, open, err = d.Open(context.Background(), t.TempDir(), "", nil)
	if err != nil || open {
		t.Fatalf("other dir: open=%v err=%v", open, err)
	}
}
