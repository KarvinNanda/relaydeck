//go:build unix

package agent

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/launch"
)

func TestForcedStopKillsProcessGroup(t *testing.T) {
	m := newMgr(t, nil)
	a, err := m.Start("g", launch.Command{
		Argv: []string{"sh", "-c", `trap "" HUP; sleep 30 & echo CHILD:$!; wait`},
		Env:  os.Environ(), Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, out, cancel := a.Subscribe()
	defer cancel()
	acc := collect(t, replay, out, "CHILD:")
	m2 := regexp.MustCompile(`CHILD:(\d+)`).FindSubmatch(bytes.TrimSpace(acc))
	pid, _ := strconv.Atoi(string(m2[1]))
	a.Stop(200 * time.Millisecond)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("grandchild survived a forced Stop")
}
