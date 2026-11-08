package auth

import (
	"strings"
	"testing"
	"time"
)

func TestJWTRoundTripExpiryAndTamper(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	tok := SignJWT(key, Claims{Sub: "u1", Name: "ana", Sid: "s1"}, now, AccessTTL)
	if !LooksLikeJWT(tok) {
		t.Fatalf("not JWT-shaped: %s", tok)
	}
	c, err := VerifyJWT(key, tok, now.Add(time.Minute))
	if err != nil || c.Sub != "u1" || c.Sid != "s1" {
		t.Fatalf("verify: %+v %v", c, err)
	}
	if exp, ok := UnverifiedExpiry(tok); !ok || !exp.Equal(now.Add(AccessTTL)) {
		t.Fatalf("expiry = %v", exp)
	}
	// Expired at exactly exp.
	if _, err := VerifyJWT(key, tok, now.Add(AccessTTL)); err != ErrJWTExpired {
		t.Fatalf("expired token: %v", err)
	}
	// Another key, a flipped payload, and alg:none are all refused.
	if _, err := VerifyJWT([]byte("another-key-another-key-another-k"), tok, now); err != ErrJWTSignature {
		t.Fatalf("wrong key: %v", err)
	}
	parts := strings.Split(tok, ".")
	forged := SignJWT(key, Claims{Sub: "admin", Sid: "s1"}, now, AccessTTL)
	if _, err := VerifyJWT(key, parts[0]+"."+strings.Split(forged, ".")[1]+"."+parts[2], now); err != ErrJWTSignature {
		t.Fatalf("swapped payload: %v", err)
	}
	none := b64([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + "."
	if _, err := VerifyJWT(key, none, now); err == nil {
		t.Fatal("alg:none accepted")
	}
}
