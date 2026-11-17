package agent

import "sync"

// ring keeps the most recent bytes written to it, up to a fixed size.
type ring struct {
	mu   sync.Mutex
	buf  []byte
	size int
}

func newRing(size int) *ring { return &ring{size: size} }

func (r *ring) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) >= r.size {
		r.buf = append(r.buf[:0], p[len(p)-r.size:]...)
		return
	}
	if over := len(r.buf) + len(p) - r.size; over > 0 {
		r.buf = append(r.buf[:0], r.buf[over:]...)
	}
	r.buf = append(r.buf, p...)
}

// Bytes returns a copy of the current contents.
func (r *ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf...)
}

// release drops the buffer. The agent is stopped, so nothing writes again.
func (r *ring) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = nil
}
