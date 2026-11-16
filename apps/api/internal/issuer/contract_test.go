package issuer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The contract, rule by rule, on tokens this test mints with a key it
// generates. The golden vectors pin agreement with the issuer's reference
// implementation; this suite pins every rule on its own.

const (
	tIss = "https://issuer.example.test"
	tAud = "product-a"
	tKid = "k1"
)

var tNow = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

type minter struct {
	key *ecdsa.PrivateKey
}

func newMinter(t *testing.T) *minter {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &minter{key: k}
}

func b64j(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func claims() map[string]any {
	return map[string]any{
		"iss": tIss, "aud": tAud, "sub": "acct-1", "sid": "sess-1", "tid": "tenant-1", "jti": "j1",
		"iat": tNow.Add(-time.Minute).Unix(), "nbf": tNow.Add(-time.Minute).Unix(),
		"exp": tNow.Add(8 * time.Minute).Unix(), "auth_time": tNow.Add(-time.Hour).Unix(),
		"role": "owner",
	}
}

func (m *minter) es256(t *testing.T, hdr map[string]any, c map[string]any) string {
	t.Helper()
	in := b64j(hdr) + "." + b64j(c)
	d := sha256.Sum256([]byte(in))
	r, s, err := ecdsa.Sign(rand.Reader, m.key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (m *minter) token(t *testing.T, mutate func(map[string]any)) string {
	c := claims()
	if mutate != nil {
		mutate(c)
	}
	return m.es256(t, map[string]any{"alg": "ES256", "kid": tKid, "typ": "at+jwt"}, c)
}

func (m *minter) verifier() *Verifier {
	keys := KeySet{tKid: &m.key.PublicKey}
	return &Verifier{Issuer: tIss, Audience: tAud,
		Keys: func(string) (KeySet, error) { return keys, nil },
		Now:  func() time.Time { return tNow }}
}

func TestAValidTokenGivesItsClaimsAndNoRole(t *testing.T) {
	m := newMinter(t)
	c, err := m.verifier().Verify(m.token(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Tenant != "tenant-1" || c.Subject != "acct-1" || c.Session != "sess-1" || c.AuthTime.Unix() != tNow.Add(-time.Hour).Unix() {
		t.Fatalf("claims: %+v", c)
	}
	// An audience list that contains ours is accepted.
	if _, err := m.verifier().Verify(m.token(t, func(c map[string]any) { c["aud"] = []string{"x", tAud} })); err != nil {
		t.Fatalf("aud list: %v", err)
	}
}

func TestEveryRequiredClaimIsRequired(t *testing.T) {
	m := newMinter(t)
	for _, k := range []string{"exp", "iat", "auth_time", "sub", "sid", "tid"} {
		tok := m.token(t, func(c map[string]any) { delete(c, k) })
		if _, err := m.verifier().Verify(tok); !errors.Is(err, ErrMissingClaim) {
			t.Errorf("without %s: %v", k, err)
		}
		tok = m.token(t, func(c map[string]any) { c[k] = "" })
		if _, err := m.verifier().Verify(tok); err == nil {
			t.Errorf("empty %s accepted", k)
		}
	}
}

func TestIssuerAudienceAndTime(t *testing.T) {
	m := newMinter(t)
	cases := []struct {
		name string
		mut  func(map[string]any)
		want error
	}{
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://other.example.test" }, ErrIssuer},
		{"wrong audience", func(c map[string]any) { c["aud"] = "another-product" }, ErrAudience},
		{"audience list without ours", func(c map[string]any) { c["aud"] = []string{"x", "y"} }, ErrAudience},
		{"expired beyond skew", func(c map[string]any) { c["exp"] = tNow.Add(-61 * time.Second).Unix() }, ErrExpired},
		{"not before, beyond skew", func(c map[string]any) { c["nbf"] = tNow.Add(2 * time.Minute).Unix() }, ErrNotYetValid},
		{"issued in the future", func(c map[string]any) {
			c["iat"] = tNow.Add(2 * time.Minute).Unix()
			c["exp"] = tNow.Add(9 * time.Minute).Unix()
		}, ErrNotYetValid},
		{"authenticated in the future", func(c map[string]any) { c["auth_time"] = tNow.Add(2 * time.Minute).Unix() }, ErrNotYetValid},
		{"lifetime over ten minutes", func(c map[string]any) { c["exp"] = tNow.Add(10 * time.Minute).Unix() }, ErrLifetime},
	}
	for _, tc := range cases {
		if _, err := m.verifier().Verify(m.token(t, tc.mut)); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	// Inside the skew is accepted.
	if _, err := m.verifier().Verify(m.token(t, func(c map[string]any) { c["exp"] = tNow.Add(-59 * time.Second).Unix() })); err != nil {
		t.Errorf("expired within skew refused: %v", err)
	}
}

// Revocation compares auth_time, never iat: a token issued AFTER the
// revocation from a sign-in BEFORE it is refused, and one from a sign-in
// after it is accepted.
func TestRevocationComparesAuthTimeNotIssuedAt(t *testing.T) {
	m := newMinter(t)
	v := m.verifier()
	cut := tNow.Add(-30 * time.Minute)
	v.RevokedBefore = func(tid, sub, sid string) (time.Time, bool) {
		return cut, tid == "tenant-1" && sub == "acct-1"
	}
	old := m.token(t, func(c map[string]any) { c["auth_time"] = cut.Add(-time.Second).Unix() })
	if _, err := v.Verify(old); !errors.Is(err, ErrRevoked) {
		t.Fatalf("a fresh token from a revoked sign-in: %v", err)
	}
	newer := m.token(t, func(c map[string]any) {
		c["auth_time"] = cut.Add(time.Second).Unix()
		c["iat"] = cut.Add(-time.Hour).Unix() // iat must not matter
		c["exp"] = cut.Add(-time.Hour + 9*time.Minute).Unix()
	})
	if _, err := v.Verify(newer); errors.Is(err, ErrRevoked) {
		t.Fatalf("revocation read iat: %v", err)
	}
}

// Algorithm confusion. The verifier decides the algorithm; the header can
// only agree with it.
func TestOnlyES256IsAccepted(t *testing.T) {
	m := newMinter(t)
	body := b64j(claims())
	// alg none, unsigned and with an empty signature.
	for _, sig := range []string{"", "x"} {
		tok := b64j(map[string]any{"alg": "none", "kid": tKid}) + "." + body + "." + sig
		if _, err := m.verifier().Verify(tok); !errors.Is(err, ErrAlgorithm) {
			t.Errorf("alg none: %v", err)
		}
	}
	// HS256 keyed with the public key, as raw point, SPKI DER and SPKI PEM.
	der, _ := x509.MarshalPKIXPublicKey(&m.key.PublicKey)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	raw := elliptic.Marshal(elliptic.P256(), m.key.PublicKey.X, m.key.PublicKey.Y) //nolint:staticcheck
	for i, k := range [][]byte{pemKey, der, raw} {
		in := b64j(map[string]any{"alg": "HS256", "kid": tKid}) + "." + body
		mac := hmac.New(sha256.New, k)
		mac.Write([]byte(in))
		tok := in + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if _, err := m.verifier().Verify(tok); !errors.Is(err, ErrAlgorithm) {
			t.Errorf("HS256 with public key form %d: %v", i, err)
		}
	}
	// Every other alg, including lower/upper-case variants, with a valid
	// ES256 signature underneath.
	for _, alg := range []string{"ES384", "ES512", "RS256", "PS256", "HS384", "EdDSA", "es256", "ES256 ", ""} {
		tok := m.es256(t, map[string]any{"alg": alg, "kid": tKid}, claims())
		if _, err := m.verifier().Verify(tok); !errors.Is(err, ErrAlgorithm) {
			t.Errorf("alg %q: %v", alg, err)
		}
	}
}

func TestKeyAndSignature(t *testing.T) {
	m := newMinter(t)
	for _, kid := range []string{"", "other"} {
		tok := m.es256(t, map[string]any{"alg": "ES256", "kid": kid}, claims())
		if _, err := m.verifier().Verify(tok); !errors.Is(err, ErrUnknownKey) {
			t.Errorf("kid %q: %v", kid, err)
		}
	}
	// Signed by another key under our kid.
	other := newMinter(t)
	if _, err := m.verifier().Verify(other.token(t, nil)); !errors.Is(err, ErrSignature) {
		t.Errorf("foreign key: %v", err)
	}
	// A payload changed after signing.
	tok := m.token(t, nil)
	p := strings.Split(tok, ".")
	c := claims()
	c["tid"] = "tenant-2"
	if _, err := m.verifier().Verify(p[0] + "." + b64j(c) + "." + p[2]); !errors.Is(err, ErrSignature) {
		t.Errorf("tampered tid: %v", err)
	}
	// DER-encoded signature instead of r||s.
	if _, err := m.verifier().Verify(p[0] + "." + p[1] + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 70))); !errors.Is(err, ErrSignature) {
		t.Errorf("wrong-length signature: %v", err)
	}
	// A validly signed token over the size cap is refused.
	big := m.token(t, func(c map[string]any) { c["pad"] = strings.Repeat("x", 9000) })
	if _, err := m.verifier().Verify(big); !errors.Is(err, ErrMalformed) {
		t.Errorf("oversized token: %v", err)
	}
	for _, bad := range []string{"", "a.b", "a.b.c.d", strings.Repeat("a", 9000)} {
		if _, err := m.verifier().Verify(bad); err == nil {
			t.Errorf("malformed %q accepted", bad[:min(len(bad), 10)])
		}
	}
	if _, err := (&Verifier{}).Verify(tok); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("unconfigured verifier: %v", err)
	}
}

func jwkOf(pub *ecdsa.PublicKey, kid string) map[string]any {
	return map[string]any{"kty": "EC", "crv": "P-256", "kid": kid, "use": "sig", "alg": "ES256",
		"x": base64.RawURLEncoding.EncodeToString(pub.X.FillBytes(make([]byte, 32))),
		"y": base64.RawURLEncoding.EncodeToString(pub.Y.FillBytes(make([]byte, 32)))}
}

func TestJWKSKeepsOnlyPublicP256SigningKeys(t *testing.T) {
	m := newMinter(t)
	good := jwkOf(&m.key.PublicKey, "good")
	withD := jwkOf(&m.key.PublicKey, "with-d")
	withD["d"] = "AAAA"
	enc := jwkOf(&m.key.PublicKey, "enc")
	enc["use"] = "enc"
	rs := jwkOf(&m.key.PublicKey, "rs")
	rs["alg"] = "RS256"
	p384 := jwkOf(&m.key.PublicKey, "p384")
	p384["crv"] = "P-384"
	off := jwkOf(&m.key.PublicKey, "off")
	off["y"] = base64.RawURLEncoding.EncodeToString(new(big.Int).Add(m.key.PublicKey.Y, big.NewInt(1)).FillBytes(make([]byte, 32)))
	doc, _ := json.Marshal(map[string]any{"keys": []any{good, withD, enc, rs, p384, off}})
	ks, err := ParseJWKS(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 1 || ks["good"] == nil {
		t.Fatalf("kept: %v", keysOf(ks))
	}
	only, _ := json.Marshal(map[string]any{"keys": []any{withD}})
	if _, err := ParseJWKS(only); err == nil {
		t.Fatal("a set holding only a private key was accepted")
	}
}

func keysOf(ks KeySet) []string {
	var out []string
	for k := range ks {
		out = append(out, k)
	}
	return out
}

func TestJWKSURLMustBeHTTPS(t *testing.T) {
	for _, u := range []string{"https://issuer.example.test/jwks", "http://127.0.0.1:9/jwks", "http://localhost/jwks"} {
		if err := CheckJWKSURL(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range []string{"http://issuer.example.test/jwks", "https://u:p@issuer.example.test/jwks", "ftp://x/jwks", "/jwks", ""} {
		if err := CheckJWKSURL(u); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

// An unknown kid refetches the set at most once per MinRefetch; a failed
// refetch serves the last good set until MaxStale, then fails closed.
func TestJWKSRefetchIsRateLimitedAndFailsClosedWhenStale(t *testing.T) {
	m := newMinter(t)
	var hits atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if down.Load() {
			http.Error(w, "down", 503)
			return
		}
		doc, _ := json.Marshal(map[string]any{"keys": []any{jwkOf(&m.key.PublicKey, tKid)}})
		w.Write(doc)
	}))
	defer srv.Close()
	now := tNow
	j := &JWKS{URL: srv.URL, Client: srv.Client(), TTL: 10 * time.Minute, MinRefetch: 30 * time.Second, MaxStale: time.Hour,
		Now: func() time.Time { return now }}
	if _, err := j.KeysFor(tKid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		j.KeysFor(fmt.Sprintf("made-up-%d", i))
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d fetches for a burst of unknown kids inside MinRefetch, want 1", n)
	}
	now = now.Add(31 * time.Second)
	j.KeysFor("made-up")
	if n := hits.Load(); n != 2 {
		t.Fatalf("%d fetches after MinRefetch, want 2", n)
	}
	down.Store(true)
	now = now.Add(30 * time.Minute)
	if _, err := j.KeysFor(tKid); err != nil {
		t.Fatalf("stale-but-usable set refused: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := j.KeysFor(tKid); err == nil {
		t.Fatal("a set older than MaxStale was still served")
	}
}

// Before any successful fetch, an unreachable issuer is still asked at most
// once per MinRefetch, and a redirect is not followed.
func TestJWKSColdStartIsRateLimitedAndRedirectsAreRefused(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/jwks", http.StatusFound)
			return
		}
		http.Error(w, "down", 503)
	}))
	defer srv.Close()
	now := tNow
	j := &JWKS{URL: srv.URL + "/jwks", Client: srv.Client(), MinRefetch: 30 * time.Second, Now: func() time.Time { return now }}
	for i := 0; i < 50; i++ {
		if _, err := j.KeysFor(tKid); err == nil {
			t.Fatal("keys from a down issuer")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d fetches from a cold cache inside MinRefetch, want 1", n)
	}
	hits.Store(0)
	r := &JWKS{URL: srv.URL + "/moved", Client: srv.Client(), Now: func() time.Time { return now }}
	if _, err := r.KeysFor(tKid); err == nil {
		t.Fatal("a redirected key set was accepted")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("the redirect was followed: %d requests", n)
	}
}

// A short or long signature is ErrSignature, never a panic: the length check
// is what keeps sig[:32] in range.
func TestAShortOrLongSignatureIsRefusedWithoutPanic(t *testing.T) {
	m := newMinter(t)
	p := strings.Split(m.token(t, nil), ".")
	for _, n := range []int{0, 1, 31, 32, 63, 65, 128} {
		tok := p[0] + "." + p[1] + "." + base64.RawURLEncoding.EncodeToString(make([]byte, n))
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%d-byte signature panicked: %v", n, r)
				}
			}()
			if _, err := m.verifier().Verify(tok); !errors.Is(err, ErrSignature) {
				t.Errorf("%d-byte signature: %v", n, err)
			}
		}()
	}
}
