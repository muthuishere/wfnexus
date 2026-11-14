package api

import (
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
)

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

	// Per test: what the token and userinfo say.
	alg        string // "RS256" (default), "ES256", "none", "HS256"
	badSig     bool   // sign with a key the set does not publish
	noIDToken  bool
	sub        string // userinfo sub; "" = same as the id_token's
	claimsEdit func(c map[string]any)
}

func newMockOP(t *testing.T, secret string) *mockOP {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	m := &mockOP{t: t, secret: secret, rsa: rk, ec: ek, alg: "RS256"}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockOP) URL() string { return m.srv.URL }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (m *mockOP) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": m.srv.URL, "authorization_endpoint": m.srv.URL + "/authorize",
			"token_endpoint": m.srv.URL + "/token", "userinfo_endpoint": m.srv.URL + "/userinfo",
			"jwks_uri": m.srv.URL + "/jwks",
		})
	case "/jwks":
		e := big.NewInt(int64(m.rsa.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "rsa1", "use": "sig", "n": b64(m.rsa.N.Bytes()), "e": b64(e)},
			{"kty": "EC", "kid": "ec1", "use": "sig", "crv": "P-256",
				"x": b64(m.ec.X.FillBytes(make([]byte, 32))), "y": b64(m.ec.Y.FillBytes(make([]byte, 32)))},
		}})
	case "/authorize":
		q := r.URL.Query()
		m.mu.Lock()
		m.challenge, m.nonce = q.Get("code_challenge"), q.Get("nonce")
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
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
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
		sub := m.sub
		if sub == "" {
			sub = "42"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": sub, "id": 42, "login": "octo", "preferred_username": "bob", "groups": []string{"admins"}})
	default:
		w.WriteHeader(404)
	}
}

// idToken is a token for this sign-in, shaped by the test's knobs.
func (m *mockOP) idToken(nonce string) string {
	now := time.Now().Unix()
	c := map[string]any{"iss": m.srv.URL, "sub": "42", "aud": "wfx", "exp": now + 300, "iat": now, "nonce": nonce}
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
	case "none":
		sig = nil
	}
	return input + "." + b64(sig)
}

// oauthFlow drives one sign-in through h: start, the browser at the
// provider's authorize URL, the callback. It returns the callback's response
// and the start response's cookies.
func oauthFlow(t *testing.T, h http.Handler, provider string, edit func(cookies []*http.Cookie) []*http.Cookie) (*httptest.ResponseRecorder, *url.URL) {
	t.Helper()
	start := httptest.NewRecorder()
	h.ServeHTTP(start, httptest.NewRequest("GET", "/api/auth/oauth/"+provider+"/start", nil))
	if start.Code != 302 {
		t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	loc, _ := url.Parse(start.Header().Get("Location"))
	// The browser visits the provider, which records the challenge and nonce.
	if res, err := http.Get(loc.String()); err != nil || res.StatusCode != 200 {
		t.Fatalf("authorize: %v %v", res, err)
	} else {
		res.Body.Close()
	}
	cookies := start.Result().Cookies()
	if edit != nil {
		cookies = edit(cookies)
	}
	req := httptest.NewRequest("GET", "/api/auth/oauth/"+provider+"/callback?code=good&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w, loc
}

func dropCookie(name string) func([]*http.Cookie) []*http.Cookie {
	return func(cs []*http.Cookie) []*http.Cookie {
		var out []*http.Cookie
		for _, c := range cs {
			if c.Name != name {
				out = append(out, c)
			}
		}
		return out
	}
}

func setCookie(name, value string) func([]*http.Cookie) []*http.Cookie {
	return func(cs []*http.Cookie) []*http.Cookie {
		out := dropCookie(name)(cs)
		return append(out, &http.Cookie{Name: name, Value: value})
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
