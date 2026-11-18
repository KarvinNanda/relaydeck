package screen

import (
	"sync"
	"time"
)

type safeBuf struct {
	mu sync.Mutex
	n  int
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n += len(p)
	return len(p), nil
}

func (b *safeBuf) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

func waitFor(f func() bool) bool {
	for i := 0; i < 200; i++ {
		if f() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
