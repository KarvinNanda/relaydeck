//go:build unix

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

func watchResize() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
			if err == nil {
				fmt.Printf("SIZE:%dx%d\n", ws.Col, ws.Row)
			}
		}
	}()
}
