package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// The access credential (ADR 0028): an HS256 JWT, short-lived, carrying the
// user id and the refresh row (sid) it was minted from. Hand-rolled because
// HS256 is twenty lines and one fixed algorithm is the whole point: the header
// is never read for anything but a check that it says HS256, so "alg":"none"
// and key-confusion tricks have nothing to bite on.

// AccessTTL is how long an access token lives.
const AccessTTL = 15 * time.Minute

// Claims is what an access token says.
type Claims struct {
	Sub  string `json:"sub"`  // user id
	Name string `json:"name"` // user name, for display only
	Sid  string `json:"sid"`  // the user_tokens row (refresh token) it came from
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
}

var (
	ErrJWTMalformed = errors.New("malformed token")
	ErrJWTSignature = errors.New("bad token signature")
	ErrJWTExpired   = errors.New("token expired")
)

var jwtHeader = b64(mustJSON(map[string]string{"alg": "HS256", "typ": "JWT"}))

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// LooksLikeJWT reports whether a bearer has the three-part JWT shape.
func LooksLikeJWT(tok string) bool {
	return strings.Count(tok, ".") == 2 && !strings.HasPrefix(tok, "wfx_")
}

// SignJWT mints an access token valid for ttl from now.
func SignJWT(key []byte, c Claims, now time.Time, ttl time.Duration) string {
	c.Iat = now.Unix()
	c.Exp = now.Add(ttl).Unix()
	body := jwtHeader + "." + b64(mustJSON(c))
	return body + "." + b64(sign(key, body))
}

func sign(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// VerifyJWT checks the signature first, then expiry. The claims are returned
// alongside ErrJWTExpired so a caller can tell "expired" from "forged".
func VerifyJWT(key []byte, tok string, now time.Time) (*Claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] != jwtHeader {
		return nil, ErrJWTMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrJWTMalformed
	}
	if !hmac.Equal(sig, sign(key, parts[0]+"."+parts[1])) {
		return nil, ErrJWTSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrJWTMalformed
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil || c.Sub == "" || c.Sid == "" {
		return nil, ErrJWTMalformed
	}
	if now.Unix() >= c.Exp {
		return &c, ErrJWTExpired
	}
	return &c, nil
}

// UnverifiedExpiry reads exp WITHOUT checking the signature. Only for a client
// deciding when to refresh its own token; never for an authorization decision.
func UnverifiedExpiry(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var c Claims
	if json.Unmarshal(raw, &c) != nil || c.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0), true
}

// DeriveKey derives a purpose-bound key from a master key.
func DeriveKey(master []byte, label string) []byte { return sign(master, label) }
