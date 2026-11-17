package auth

import (
	"sync"
	"time"
)

const ticketTTL = 30 * time.Second

// TicketStore holds short-lived, single-use WebSocket tickets.
// Browsers cannot send headers on a WebSocket upgrade, so the UI trades its
// bearer token for a ticket and puts the ticket in the query string.
type TicketStore struct {
	mu      sync.Mutex
	now     func() time.Time
	tickets map[string]time.Time // ticket -> expiry
}

func NewTicketStore(now func() time.Time) *TicketStore {
	return &TicketStore{now: now, tickets: make(map[string]time.Time)}
}

// Issue creates a ticket valid for 30 seconds. It also drops expired tickets
// so the map cannot grow without bound.
func (s *TicketStore) Issue() (string, error) {
	t, err := NewToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, exp := range s.tickets {
		if !now.Before(exp) {
			delete(s.tickets, k)
		}
	}
	s.tickets[t] = now.Add(ticketTTL)
	return t, nil
}

// Redeem reports whether t is a live ticket. The ticket is deleted either way,
// so a ticket can never be used twice.
func (s *TicketStore) Redeem(t string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tickets[t]
	if !ok {
		return false
	}
	delete(s.tickets, t)
	return s.now().Before(exp)
}

func (s *TicketStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tickets)
}
