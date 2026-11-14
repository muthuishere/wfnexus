package login

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// idTokenSkew is the clock difference allowed on exp and iat.
const idTokenSkew = 60 * time.Second

// idClaims are the id_token claims a sign-in relies on.
type idClaims struct {
	Subject string
}

// verifyIDToken checks a compact JWS id_token: the algorithm (RS256 or ES256
// only), the signature against the issuer's key set, then iss, aud (and azp
// when aud names several parties), exp, iat and nonce.
func verifyIDToken(ctx context.Context, raw string, keys *keySet, issuer, clientID, nonce string, now time.Time) (*idClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a signed token")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	if hdr.Alg != "RS256" && hdr.Alg != "ES256" {
		return nil, fmt.Errorf("signing algorithm %q is not accepted (RS256 or ES256)", hdr.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("signature is not base64url")
	}
	key, err := keys.key(ctx, hdr.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch hdr.Alg {
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok || rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) != nil {
			return nil, errors.New("signature does not verify")
		}
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok || pub.Curve != elliptic.P256() || len(sig) != 64 ||
			!ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return nil, errors.New("signature does not verify")
		}
	}

	var c struct {
		Iss   string          `json:"iss"`
		Sub   string          `json:"sub"`
		Aud   json.RawMessage `json:"aud"`
		Azp   string          `json:"azp"`
		Exp   *json.Number    `json:"exp"`
		Iat   *json.Number    `json:"iat"`
		Nonce string          `json:"nonce"`
	}
	if err := decodeSegment(parts[1], &c); err != nil {
		return nil, fmt.Errorf("claims: %w", err)
	}
	if c.Iss != issuer {
		return nil, fmt.Errorf("issuer %q is not %q", c.Iss, issuer)
	}
	var aud []string
	if json.Unmarshal(c.Aud, &aud) != nil {
		var one string
		if json.Unmarshal(c.Aud, &one) != nil || one == "" {
			return nil, errors.New("no audience")
		}
		aud = []string{one}
	}
	found := false
	for _, a := range aud {
		found = found || a == clientID
	}
	if !found {
		return nil, errors.New("the token is not for this client")
	}
	if len(aud) > 1 && c.Azp != clientID {
		return nil, errors.New("the token names several audiences and this client is not its authorized party")
	}
	exp, err1 := seconds(c.Exp)
	iat, err2 := seconds(c.Iat)
	if err1 != nil || err2 != nil {
		return nil, errors.New("exp and iat are required")
	}
	if now.After(exp.Add(idTokenSkew)) {
		return nil, errors.New("the token has expired")
	}
	if iat.After(now.Add(idTokenSkew)) {
		return nil, errors.New("the token was issued in the future")
	}
	if c.Nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("nonce does not match this sign-in")
	}
	if c.Sub == "" {
		return nil, errors.New("the token names no subject")
	}
	return &idClaims{Subject: c.Sub}, nil
}

func decodeSegment(seg string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return errors.New("not base64url")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	return dec.Decode(out)
}

func seconds(n *json.Number) (time.Time, error) {
	if n == nil {
		return time.Time{}, errors.New("missing")
	}
	f, err := n.Float64()
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(f), 0), nil
}

// keySet is an issuer's published signing keys (jwks_uri), cached, and
// fetched again when a token names a key it does not hold — at most once a
// minute, so a stream of unknown kids cannot turn into a stream of fetches.
type keySet struct {
	url    string
	client *http.Client

	mu    sync.Mutex
	keys  map[string]crypto.PublicKey
	fetch time.Time
}

func (s *keySet) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.lookup(kid); ok {
		return k, nil
	}
	if s.keys != nil && time.Since(s.fetch) < time.Minute {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	if k, ok := s.lookup(kid); ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

// lookup finds kid; a token naming no kid matches only a set of one key.
func (s *keySet) lookup(kid string) (crypto.PublicKey, bool) {
	if kid == "" && len(s.keys) == 1 {
		for _, k := range s.keys {
			return k, true
		}
	}
	k, ok := s.keys[kid]
	return k, ok
}

func (s *keySet) load(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("key set: %s", res.Status)
	}
	var set struct {
		Keys []struct {
			Kty, Kid, Use, Crv, N, E, X, Y string
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&set); err != nil {
		return err
	}
	keys := map[string]crypto.PublicKey{}
	b := func(s string) *big.Int {
		v, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || len(v) == 0 {
			return nil
		}
		return new(big.Int).SetBytes(v)
	}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, e := b(k.N), b(k.E)
			if n != nil && e != nil && e.IsInt64() {
				keys[k.Kid] = &rsa.PublicKey{N: n, E: int(e.Int64())}
			}
		case "EC":
			x, y := b(k.X), b(k.Y)
			if k.Crv == "P-256" && x != nil && y != nil && elliptic.P256().IsOnCurve(x, y) {
				keys[k.Kid] = &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
			}
		}
	}
	s.keys, s.fetch = keys, time.Now()
	return nil
}
