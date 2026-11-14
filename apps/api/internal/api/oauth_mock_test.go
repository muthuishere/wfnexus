package api

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// testPublicURL is the public address the OAuth tests configure.
const testPublicURL = "https://wfx.example.com"

// testServerPublic is testServerAuth with a public URL.
func testServerPublic(t *testing.T, a config.Auth, publicURL string) (http.Handler, *store.Store) {
	t.Helper()
	path := t.TempDir() + "/wfnexus.db"
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	eng := engine.New(config.Config{Addr: remoteAddr, Auth: a, PublicURL: publicURL}, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	return New(eng, st, nil, remoteAddr, "", nil), st
}

// mockOP is an OpenID Provider for tests: discovery, an authorize endpoint
// that records the PKCE challenge and nonce, a token endpoint that checks the
// code_verifier and issues an id_token signed with its own key, its key set,
// and userinfo.
type mockOP struct {
	t      *testing.T
	srv    *httptest.Server
	secret string
	rsa    *rsa.PrivateKey
	ec     *ecdsa.PrivateKey

	mu        sync.Mutex
	challenge string
	nonce     string
	gotVerif  string
	gotRedir  string

	// Per test: what discovery, the token and userinfo say.
	discIssuer string // "" = its own URL
	badPKCE    bool   // expect a challenge other than the one sent
	alg        string // "RS256" (default), "ES256", "none", "HS256"
	badSig     bool   // sign with a key the set does not publish
	noIDToken  bool
	sub        string // id_token and userinfo sub (default "42")
	uiSub      string // userinfo sub when it should differ
	userinfo   map[string]any
	claimsEdit func(c map[string]any)
}

func newMockOP(t *testing.T, secret string) *mockOP {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	m := &mockOP{t: t, secret: secret, rsa: rk, ec: ek, alg: "RS256", sub: "42",
		userinfo: map[string]any{"preferred_username": "bob", "groups": []string{"admins"}}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockOP) URL() string { return m.srv.URL }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (m *mockOP) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		iss := m.srv.URL
		if m.discIssuer != "" {
			iss = m.discIssuer
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": m.srv.URL + "/authorize",
			"token_endpoint": m.srv.URL + "/token", "userinfo_endpoint": m.srv.URL + "/userinfo",
			"jwks_uri": m.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256", "ES256"},
		})
	case "/jwks":
		e := big.NewInt(int64(m.rsa.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "rsa1", "use": "sig", "alg": "RS256", "n": b64(m.rsa.N.Bytes()), "e": b64(e)},
			{"kty": "EC", "kid": "ec1", "use": "sig", "alg": "ES256", "crv": "P-256",
				"x": b64(m.ec.X.FillBytes(make([]byte, 32))), "y": b64(m.ec.Y.FillBytes(make([]byte, 32)))},
		}})
	case "/authorize":
		q := r.URL.Query()
		m.mu.Lock()
		m.challenge, m.nonce, m.gotRedir = q.Get("code_challenge"), q.Get("nonce"), q.Get("redirect_uri")
		if m.badPKCE {
			m.challenge = b64([]byte("some-other-challenge-value-0123456789"))
		}
		m.mu.Unlock()
		if q.Get("code_challenge_method") != "S256" {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(200)
	case "/token":
		_ = r.ParseForm()
		m.mu.Lock()
		m.gotVerif = r.Form.Get("code_verifier")
		sum := sha256.Sum256([]byte(m.gotVerif))
		pkceOK := m.challenge != "" && b64(sum[:]) == m.challenge
		nonce := m.nonce
		m.mu.Unlock()
		if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != m.secret || r.Form.Get("client_id") != "wfx" || !pkceOK {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "provider-detail-text"})
			return
		}
		out := map[string]string{"access_token": "at-123", "token_type": "Bearer"}
		if !m.noIDToken {
			out["id_token"] = m.idToken(nonce)
		}
		_ = json.NewEncoder(w).Encode(out)
	case "/userinfo":
		if r.Header.Get("Authorization") != "Bearer at-123" {
			w.WriteHeader(401)
			return
		}
		out := map[string]any{"sub": m.sub}
		if m.uiSub != "" {
			out["sub"] = m.uiSub
		}
		for k, v := range m.userinfo {
			out[k] = v
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(404)
	}
}

// idToken is a token for this sign-in, shaped by the test's knobs.
func (m *mockOP) idToken(nonce string) string {
	now := time.Now().Unix()
	c := map[string]any{"iss": m.srv.URL, "sub": m.sub, "aud": "wfx", "exp": now + 300, "iat": now, "nonce": nonce}
	if m.claimsEdit != nil {
		m.claimsEdit(c)
	}
	hdr := map[string]any{"alg": m.alg, "typ": "JWT"}
	switch m.alg {
	case "RS256":
		hdr["kid"] = "rsa1"
	case "ES256":
		hdr["kid"] = "ec1"
	}
	hb, _ := json.Marshal(hdr)
	cb, _ := json.Marshal(c)
	input := b64(hb) + "." + b64(cb)
	digest := sha256.Sum256([]byte(input))
	var sig []byte
	switch m.alg {
	case "RS256":
		key := m.rsa
		if m.badSig {
			key, _ = rsa.GenerateKey(rand.Reader, 2048)
		}
		sig, _ = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	case "ES256":
		r, s, _ := ecdsa.Sign(rand.Reader, m.ec, digest[:])
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	case "HS256":
		mac := hmac.New(sha256.New, []byte("shared"))
		mac.Write([]byte(input))
		sig = mac.Sum(nil)
	}
	return input + "." + b64(sig)
}

// oauthFlow drives one sign-in through h: start (sent with a forged Host),
// the browser at the provider's authorize URL, the callback. edit may change
// the cookies the callback carries.
func oauthFlow(t *testing.T, h http.Handler, provider string, edit func(cookies []*http.Cookie) []*http.Cookie) (*httptest.ResponseRecorder, *url.URL) {
	t.Helper()
	start := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/auth/oauth/"+provider+"/start", nil)
	req.Host = "evil.example"
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("X-Forwarded-Proto", "http")
	h.ServeHTTP(start, req)
	if start.Code != 302 {
		t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	loc, _ := url.Parse(start.Header().Get("Location"))
	// The browser visits the provider, which records the challenge and nonce.
	res, err := http.Get(loc.String())
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("authorize: %v %v", res, err)
	}
	res.Body.Close()
	cookies := start.Result().Cookies()
	if edit != nil {
		cookies = edit(cookies)
	}
	cb := httptest.NewRequest("GET", "/api/auth/oauth/"+provider+"/callback?code=good&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	for _, c := range cookies {
		cb.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, cb)
	return w, loc
}

// oauthCookieName is the sign-in cookie's name under testPublicURL (https).
const oauthCookieName = "__Host-" + oauthCookieBase

func withoutCookies(cs []*http.Cookie) []*http.Cookie { return nil }

// replaceTicket swaps the sign-in cookie's value.
func replaceTicket(value string) func([]*http.Cookie) []*http.Cookie {
	return func([]*http.Cookie) []*http.Cookie {
		return []*http.Cookie{{Name: oauthCookieName, Value: value}}
	}
}

// reseal re-signs the ticket the server set, changed by edit, with key.
func reseal(key []byte, edit func(*oauthTicket)) func([]*http.Cookie) []*http.Cookie {
	return func(cs []*http.Cookie) []*http.Cookie {
		var t oauthTicket
		for _, c := range cs {
			if c.Name == oauthCookieName {
				t, _ = (&Server{jwtKey: fixedJWTKey}).openTicket(c.Value, "sso")
			}
		}
		edit(&t)
		return []*http.Cookie{{Name: oauthCookieName, Value: (&Server{jwtKey: key}).sealTicket(t)}}
	}
}

func oauthSession(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	return ""
}
