//go:build unix

package agent

import (
	"errors"
	"os"
	"syscall"
)

// killTree kills the whole process group. go-pty starts the child as a session
// leader, so its pid is the group id and this also reaches subprocesses such as
// MCP servers and shells.
func killTree(p *os.Process) error {
	err := syscall.Kill(-p.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		// Not a group leader (or already gone): fall back to the process.
		return p.Kill()
	}
	return err
}
