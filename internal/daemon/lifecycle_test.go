package daemon

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestSafeGoRecoversAndLogs(t *testing.T) {
	var logs syncBuf
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	done := make(chan struct{})
	safeGo(log, "test-loop", func() {
		defer close(done) // runs before the recover, like wg.Done in Run
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "goroutine panic") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	out := logs.String()
	if !strings.Contains(out, "goroutine panic") || !strings.Contains(out, "test-loop") || !strings.Contains(out, "stack") {
		t.Fatalf("panic not logged with name and stack: %s", out)
	}
}
