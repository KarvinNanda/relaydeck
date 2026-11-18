//go:build unix

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// termSize returns the terminal size as "COLSxROWS".
func termSize() string {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return "unknown"
	}
	return fmt.Sprintf("%dx%d", ws.Col, ws.Row)
}
