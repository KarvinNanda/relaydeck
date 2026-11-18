//go:build darwin || freebsd || netbsd || openbsd || linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// rawControlEcho stops the terminal from echoing control characters as "^[".
// Claude Code reads the bracketed paste markers and shows only the pasted
// text; with the echo on, the box would show "^[[200~text^[[201~" instead.
func rawControlEcho() {
	fd := int(os.Stdin.Fd())
	t, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return
	}
	t.Lflag &^= unix.ECHOCTL
	_ = unix.IoctlSetTermios(fd, setTermios, t)
}
