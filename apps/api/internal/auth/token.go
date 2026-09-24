// Package auth holds the mint-and-hash path for every bearer credential the
// server issues. Workers and users are separate tables on purpose, but there is
// one code path for the token itself, so the storage rule below is stated once.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// NewToken mints a credential. The `wfx_` prefix makes a leaked value
// recognisable in a scanner.
func NewToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "wfx_" + hex.EncodeToString(b)
}

// HashToken is how a token is stored and looked up. The value itself is never
// written to the database, a log or an event.
func HashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}
