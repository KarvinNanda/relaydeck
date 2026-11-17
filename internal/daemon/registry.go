package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
)

// registry maps agent tokens and agent ids to the folder they belong to.
// Each start gets a new random agent id and token. Only sha256(token) is
// kept, so a memory dump or a debug print never shows a usable token.
type registry struct {
	mu       sync.Mutex
	byHash   map[[sha256.Size]byte]string // sha256(token) -> agent id
	hashOf   map[string][sha256.Size]byte // agent id -> sha256(token), for revoke
	owners   map[string]delivery.Target   // agent id -> workspace/folder
	latestID map[delivery.Target]string   // workspace/folder -> newest agent id
}

func newRegistry() *registry {
	return &registry{
		byHash:   map[[sha256.Size]byte]string{},
		hashOf:   map[string][sha256.Size]byte{},
		owners:   map[string]delivery.Target{},
		latestID: map[delivery.Target]string{},
	}
}

// Lookup implements auth.AgentTokens. The token is hashed before the map
// lookup, so lookup time does not depend on how much of a guess matches.
func (r *registry) Lookup(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	h := sha256.Sum256([]byte(token))
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byHash[h]
	return id, ok
}

// issue creates a new agent id and token for t.
func (r *registry) issue(t delivery.Target) (id, token string, err error) {
	id, err = randHex(16)
	if err != nil {
		return "", "", err
	}
	token, err = auth.NewToken()
	if err != nil {
		return "", "", err
	}
	h := sha256.Sum256([]byte(token))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byHash[h] = id
	r.hashOf[id] = h
	r.owners[id] = t
	return id, token, nil
}

// revoke makes the agent's token invalid. The owner stays known, so events
// that arrive after the stop can still be mapped to the folder.
func (r *registry) revoke(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.hashOf[id]; ok {
		delete(r.byHash, h)
		delete(r.hashOf, id)
	}
}

// forget drops everything about an agent id (after a newer start replaced it).
func (r *registry) forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.hashOf[id]; ok {
		delete(r.byHash, h)
		delete(r.hashOf, id)
	}
	delete(r.owners, id)
}

func (r *registry) owner(id string) (delivery.Target, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.owners[id]
	return t, ok
}

// setLatest records id as the newest agent of t and returns the previous one.
func (r *registry) setLatest(t delivery.Target, id string) (prev string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev = r.latestID[t]
	r.latestID[t] = id
	return prev
}

func (r *registry) latest(t delivery.Target) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.latestID[t]
	return id, ok
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
