//go:build !windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals are the signals that end serve. SIGHUP is the closed
// terminal, which must stop the agents too, not leave them running.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
