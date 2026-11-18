//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A closed terminal (SIGHUP) ends the serve context, and the first signal
// releases the handler so a second one would use the default action.
func TestFirstSignalEndsContextAndReleases(t *testing.T) {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	released := false
	var mu sync.Mutex
	done := releaseOnFirstSignal(ctx, func() { mu.Lock(); released = true; mu.Unlock(); stop() })
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("context not ended by SIGHUP")
	}
	mu.Lock()
	defer mu.Unlock()
	if !released || ctx.Err() == nil {
		t.Fatalf("released = %v, ctx err = %v", released, ctx.Err())
	}
}
