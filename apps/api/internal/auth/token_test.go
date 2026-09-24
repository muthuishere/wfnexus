package auth

import (
	"regexp"
	"strings"
	"testing"
)

var tokenShape = regexp.MustCompile(`^wfx_[0-9a-f]{48}$`)

func TestNewTokenShape(t *testing.T) {
	tok := NewToken()
	if !tokenShape.MatchString(tok) {
		t.Fatalf("token does not match wfx_ + 48 hex: %d chars", len(tok))
	}
}

func TestNewTokenIsNotReused(t *testing.T) {
	if NewToken() == NewToken() {
		t.Fatal("two mints produced the same token")
	}
}

func TestHashTokenIsStableAndNotTheInput(t *testing.T) {
	tok := NewToken()
	h := HashToken(tok)
	if h != HashToken(tok) {
		t.Fatal("HashToken is not stable")
	}
	// The stored form must not carry the value, in whole or in part — a hash
	// that echoed its input would put a live credential in every row.
	if h == tok || strings.Contains(h, strings.TrimPrefix(tok, "wfx_")) {
		t.Fatal("HashToken returned its input")
	}
	if HashToken(tok) == HashToken(NewToken()) {
		t.Fatal("distinct tokens collided")
	}
}
