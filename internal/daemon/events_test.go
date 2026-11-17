package daemon

import (
	"context"
	"testing"
	"time"
)

// Ruling 11: a consumer that falls behind is dropped (its channel closes),
// so the UI reconnects and gets a full state snapshot instead of silently
// showing stale state.
func TestSlowSubscriberIsDropped(t *testing.T) {
	e := newEnv(t, nil)
	slow, cancelSlow := e.d.Subscribe()
	defer cancelSlow()
	fast, cancelFast := e.d.Subscribe()
	defer cancelFast()

	for i := 0; i < eventBuffer+1; i++ {
		e.d.hub.publish(Event{Type: "status", WS: "w", Folder: "a"})
		<-fast
	}
	n := 0
drain:
	for {
		select {
		case _, ok := <-slow:
			if !ok {
				break drain
			}
			n++
		case <-time.After(time.Second):
			t.Fatalf("slow subscriber not closed after %d events", n)
		}
	}
	if n != eventBuffer {
		t.Fatalf("slow subscriber got %d events before close, want %d", n, eventBuffer)
	}
	// The fast one is still subscribed.
	e.d.hub.publish(Event{Type: "status"})
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("fast subscriber was dropped too")
	}
	cancelSlow() // safe after the drop
}

// Run closes every subscription when it returns, so /ws/events sockets end
// on shutdown.
func TestRunEndClosesSubscriptions(t *testing.T) {
	e := newEnv(t, nil)
	ev, cancelEv := e.d.Subscribe()
	defer cancelEv()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.d.Run(ctx); close(done) }()
	cancel()
	<-done
	select {
	case _, ok := <-ev:
		if ok {
			t.Fatal("got an event instead of a closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription still open after Run returned")
	}
}
