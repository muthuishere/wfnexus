// Package issuer verifies access tokens minted by an EXTERNAL issuer: ES256
// JWTs signed with a key published as a JWKS.
//
// The contract it implements (version ContractVersion) is fixed here and
// checked by the conformance tests in this package. A change to the contract
// means a change to those tests first.
//
//   - The header alg is exactly ES256. "none", every HMAC alg (including HS256
//     keyed with the public key) and every other alg are refused BEFORE a key
//     is looked up.
//   - The header kid names a key in the JWKS; a P-256 EC key, nothing else.
//   - iss equals the configured issuer; aud is, or contains, the configured
//     audience.
//   - exp, iat, auth_time, sub, sid and tid are required. A token without tid
//     is refused: the tenant comes from the token, never from the request.
//   - The clock skew allowed on exp, nbf, iat and auth_time is MaxSkew.
//   - exp - iat is at most MaxTTL.
//   - Revocation compares auth_time (never iat) with a revoked-before time.
//   - Any role claim is ignored. What the caller may do is decided locally.
package issuer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// ContractVersion is the token contract these rules and the fixtures follow.
const ContractVersion = "v0.2.0"

// The refusal classes. A caller may log the class; it never learns more.
var (
	ErrMalformed     = errors.New("malformed token")
	ErrAlgorithm     = errors.New("algorithm not allowed")
	ErrUnknownKey    = errors.New("unknown key")
	ErrSignature     = errors.New("bad signature")
	ErrIssuer        = errors.New("wrong issuer")
	ErrAudience      = errors.New("wrong audience")
	ErrExpired       = errors.New("expired")
	ErrNotYetValid   = errors.New("not yet valid")
	ErrMissingClaim  = errors.New("missing claim")
	ErrLifetime      = errors.New("lifetime too long")
	ErrRevoked       = errors.New("revoked")
	ErrNotConfigured = errors.New("issuer not configured")
)

const (
	defaultMaxSkew    = 60 * time.Second
	defaultMaxTTL     = 10 * time.Minute
	maxTokenBytes     = 8 << 10
	requiredAlgorithm = "ES256"
)

// Claims is what a verified token says. Role is deliberately absent.
type Claims struct {
	Subject  string
	Session  string
	Tenant   string
	AuthTime time.Time
	IssuedAt time.Time
	Expires  time.Time
	ID       string
}

// KeySet is the issuer's public keys by kid.
type KeySet map[string]*ecdsa.PublicKey

// Verifier checks tokens of one issuer for one audience.
type Verifier struct {
	Issuer   string
	Audience string
	// Keys returns the key set that should hold kid: JWKS.KeysFor, or a
	// fixed set in tests.
	Keys func(kid string) (KeySet, error)
	// RevokedBefore, when set, returns a time: a token whose auth_time is
	// before it is refused. ok=false means no revocation applies.
	RevokedBefore func(tenant, subject, session string) (t time.Time, ok bool)
	MaxSkew       time.Duration
	MaxTTL        time.Duration
	Now           func() time.Time
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type rawClaims struct {
	Iss      string          `json:"iss"`
	Aud      json.RawMessage `json:"aud"`
	Sub      string          `json:"sub"`
	Sid      string          `json:"sid"`
	Tid      string          `json:"tid"`
	Jti      string          `json:"jti"`
	Exp      *json.Number    `json:"exp"`
	Nbf      *json.Number    `json:"nbf"`
	Iat      *json.Number    `json:"iat"`
	AuthTime *json.Number    `json:"auth_time"`
}

// Verify checks tok and returns its claims, or one of the Err* classes
// (wrapped with detail that never includes the token).
func (v *Verifier) Verify(tok string) (*Claims, error) {
	if v == nil || v.Issuer == "" || v.Audience == "" || v.Keys == nil {
		return nil, ErrNotConfigured
	}
	if len(tok) > maxTokenBytes {
		return nil, ErrMalformed
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}
	var h header
	if err := decodeJSON(parts[0], &h); err != nil {
		return nil, ErrMalformed
	}
	// The algorithm is decided by US, before any key is touched: the
	// header only has to agree.
	if h.Alg != requiredAlgorithm {
		return nil, fmt.Errorf("%w: %q", ErrAlgorithm, h.Alg)
	}
	keys, err := v.Keys(h.Kid)
	if err != nil {
		return nil, fmt.Errorf("%w: key set unavailable", ErrUnknownKey)
	}
	key := keys[h.Kid]
	if h.Kid == "" || key == nil {
		return nil, ErrUnknownKey
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, ErrSignature
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(key, digest[:], r, s) {
		return nil, ErrSignature
	}

	var c rawClaims
	if err := decodeJSON(parts[1], &c); err != nil {
		return nil, ErrMalformed
	}
	if c.Iss != v.Issuer {
		return nil, ErrIssuer
	}
	if !audienceHas(c.Aud, v.Audience) {
		return nil, ErrAudience
	}
	exp, okE := unix(c.Exp)
	iat, okI := unix(c.Iat)
	at, okA := unix(c.AuthTime)
	switch {
	case !okE:
		return nil, fmt.Errorf("%w: exp", ErrMissingClaim)
	case !okI:
		return nil, fmt.Errorf("%w: iat", ErrMissingClaim)
	case !okA:
		return nil, fmt.Errorf("%w: auth_time", ErrMissingClaim)
	case c.Sub == "":
		return nil, fmt.Errorf("%w: sub", ErrMissingClaim)
	case c.Sid == "":
		return nil, fmt.Errorf("%w: sid", ErrMissingClaim)
	case c.Tid == "":
		return nil, fmt.Errorf("%w: tid", ErrMissingClaim)
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	skew := v.MaxSkew
	if skew <= 0 {
		skew = defaultMaxSkew
	}
	ttl := v.MaxTTL
	if ttl <= 0 {
		ttl = defaultMaxTTL
	}
	if !now.Before(exp.Add(skew)) {
		return nil, ErrExpired
	}
	if nbf, ok := unix(c.Nbf); ok && now.Add(skew).Before(nbf) {
		return nil, ErrNotYetValid
	}
	if now.Add(skew).Before(iat) || now.Add(skew).Before(at) {
		return nil, ErrNotYetValid
	}
	if exp.Sub(iat) > ttl {
		return nil, ErrLifetime
	}
	if v.RevokedBefore != nil {
		if rb, ok := v.RevokedBefore(c.Tid, c.Sub, c.Sid); ok && at.Before(rb) {
			return nil, ErrRevoked
		}
	}
	return &Claims{Subject: c.Sub, Session: c.Sid, Tenant: c.Tid, AuthTime: at, IssuedAt: iat, Expires: exp, ID: c.Jti}, nil
}

// LooksES256 reports whether a JWT's header claims ES256, without trusting
// it: it only routes a token to this verifier rather than another one.
func LooksES256(tok string) bool {
	i := strings.IndexByte(tok, '.')
	if i <= 0 {
		return false
	}
	var h header
	return decodeJSON(tok[:i], &h) == nil && h.Alg == requiredAlgorithm
}

func decodeJSON(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	return d.Decode(v)
}

func audienceHas(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

func unix(n *json.Number) (time.Time, bool) {
	if n == nil {
		return time.Time{}, false
	}
	f, err := n.Float64()
	if err != nil || f <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// ---- JWKS ----

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	D   string `json:"d"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

// ParseJWKS reads a JWKS document into the P-256 signing keys it holds. A key
// of another type or curve, one marked for encryption, one whose alg is not
// ES256, one carrying a private part, or one whose point is not on the curve
// is skipped, never trusted.
func ParseJWKS(doc []byte) (KeySet, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(doc, &set); err != nil {
		return nil, err
	}
	out := KeySet{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" || k.D != "" ||
			(k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != requiredAlgorithm) {
			continue
		}
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			continue
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, errors.New("no usable ES256 key in the key set")
	}
	return out, nil
}
