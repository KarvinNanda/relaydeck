// Package auth holds the tokens and request guards that protect the daemon.
package auth

import (
	"crypto/rand"
	"encoding/base64"
)

// NewToken returns 32 random bytes from crypto/rand, base64url without padding.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
