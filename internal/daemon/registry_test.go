package daemon

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/delivery"
)

var _ auth.AgentTokens = (*registry)(nil)

func TestRegistryIssueLookupRevoke(t *testing.T) {
	r := newRegistry()
	ta := delivery.Target{WS: "w", Folder: "a"}
	idA, tokA, err := r.issue(ta)
	if err != nil {
		t.Fatal(err)
	}
	idB, tokB, err := r.issue(delivery.Target{WS: "w", Folder: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB || tokA == tokB || idA == "" || tokA == "" {
		t.Fatalf("ids %q %q tokens must be unique and non-empty", idA, idB)
	}
	if got, ok := r.Lookup(tokA); !ok || got != idA {
		t.Fatalf("Lookup(tokA) = %q,%v want %q", got, ok, idA)
	}
	if got, ok := r.Lookup(tokB); !ok || got != idB {
		t.Fatalf("Lookup(tokB) = %q,%v", got, ok)
	}
	for _, bad := range []string{"", "nope", tokA[:10], idA} {
		if _, ok := r.Lookup(bad); ok {
			t.Fatalf("Lookup(%q) succeeded", bad)
		}
	}
	if got, ok := r.owner(idA); !ok || got != ta {
		t.Fatalf("owner = %+v,%v", got, ok)
	}

	r.revoke(idA)
	r.revoke(idA) // idempotent
	if _, ok := r.Lookup(tokA); ok {
		t.Fatal("revoked token still valid")
	}
	if _, ok := r.Lookup(tokB); !ok {
		t.Fatal("revoking A also revoked B")
	}
	// The owner stays known after revoke, so a late "stopped" event can still
	// be mapped to its folder.
	if _, ok := r.owner(idA); !ok {
		t.Fatal("owner forgotten on revoke")
	}
}

// Only sha256(token) is kept: the plain token must not be a key or a value.
func TestRegistryStoresOnlyHash(t *testing.T) {
	r := newRegistry()
	_, tok, err := r.issue(delivery.Target{WS: "w", Folder: "a"})
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(tok))
	if _, ok := r.byHash[want]; !ok {
		t.Fatal("token hash not stored")
	}
	for h, id := range r.byHash {
		if strings.Contains(string(h[:]), tok) || strings.Contains(id, tok) {
			t.Fatal("plain token stored")
		}
	}
}

func TestRegistryLatestAndForget(t *testing.T) {
	r := newRegistry()
	tg := delivery.Target{WS: "w", Folder: "a"}
	if _, ok := r.latest(tg); ok {
		t.Fatal("latest set before any start")
	}
	id1, _, _ := r.issue(tg)
	if prev := r.setLatest(tg, id1); prev != "" {
		t.Fatalf("prev = %q", prev)
	}
	id2, _, _ := r.issue(tg)
	if prev := r.setLatest(tg, id2); prev != id1 {
		t.Fatalf("prev = %q want %q", prev, id1)
	}
	if got, _ := r.latest(tg); got != id2 {
		t.Fatalf("latest = %q", got)
	}
	r.forget(id1)
	if _, ok := r.owner(id1); ok {
		t.Fatal("forget kept the owner")
	}
}
