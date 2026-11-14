package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

func oidcServer(t *testing.T, op *mockOP) http.Handler {
	t.Helper()
	t.Setenv("TEST_SSO_SECRET", "s3cret-value")
	h, _ := testServerAuth(t, remoteAddr, config.Auth{Providers: []config.AuthProvider{{
		Name: "sso", Type: "oidc", Issuer: op.URL(), ClientID: "wfx", ClientSecretEnv: "TEST_SSO_SECRET",
		GroupRoles: map[string]string{"admins": "superadmin"},
	}}})
	return h
}

// The authorize redirect carries a PKCE S256 challenge and a nonce; the token
// request carries the verifier; a valid id_token signs the person in, and the
// sign-in's three cookies are cleared.
func TestOIDCSignInSendsPKCEAndNonceAndClearsItsCookies(t *testing.T) {
	for _, alg := range []string{"RS256", "ES256"} {
		t.Run(alg, func(t *testing.T) {
			op := newMockOP(t, "s3cret-value")
			op.alg = alg
			h := oidcServer(t, op)
			w, loc := oauthFlow(t, h, "sso", nil)
			q := loc.Query()
			if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
				t.Fatalf("authorize URL: %s", loc)
			}
			if len(op.gotVerif) < 43 {
				t.Fatalf("token request verifier = %q", op.gotVerif)
			}
			if w.Code != 303 {
				t.Fatalf("callback: %d %s", w.Code, w.Body)
			}
			cleared := map[string]bool{}
			for _, c := range w.Result().Cookies() {
				if c.MaxAge < 0 && c.Path == oauthCookiePath {
					cleared[c.Name] = true
				}
			}
			for _, name := range oauthCookies {
				if !cleared[name] {
					t.Errorf("cookie %s was not cleared after the callback", name)
				}
			}
			who := decode(t, withCookie(h, "GET", "/api/whoami", oauthSession(w), nil))
			if who["name"] != "bob@sso" || who["role"] != RoleSuperadmin {
				t.Fatalf("whoami: %v", who)
			}
		})
	}
}

// Each of these is refused, and nobody is signed in.
func TestOIDCSignInRefusals(t *testing.T) {
	for _, c := range []struct {
		name   string
		setup  func(op *mockOP)
		cookie func(op *mockOP) func([]*http.Cookie) []*http.Cookie
		want   int
	}{
		{"no verifier cookie", nil, func(*mockOP) func([]*http.Cookie) []*http.Cookie { return dropCookie(oauthVerifierCookie) }, 400},
		{"wrong verifier", nil, func(*mockOP) func([]*http.Cookie) []*http.Cookie {
			return setCookie(oauthVerifierCookie, "x123456789012345678901234567890123456789012345")
		}, 401},
		{"bad signature", func(op *mockOP) { op.badSig = true }, nil, 401},
		{"wrong issuer", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["iss"] = "https://elsewhere.example" } }, nil, 401},
		{"wrong audience", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["aud"] = "someone-else" } }, nil, 401},
		{"several audiences, no azp", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["aud"] = []string{"wfx", "other"} } }, nil, 401},
		{"expired", func(op *mockOP) {
			op.claimsEdit = func(c map[string]any) { c["exp"] = time.Now().Add(-10 * time.Minute).Unix() }
		}, nil, 401},
		{"issued in the future", func(op *mockOP) {
			op.claimsEdit = func(c map[string]any) { c["iat"] = time.Now().Add(10 * time.Minute).Unix() }
		}, nil, 401},
		{"wrong nonce", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["nonce"] = "not-this-sign-in" } }, nil, 401},
		{"no nonce cookie", nil, func(*mockOP) func([]*http.Cookie) []*http.Cookie { return dropCookie(oauthNonceCookie) }, 401},
		{"alg none", func(op *mockOP) { op.alg = "none" }, nil, 401},
		{"alg HS256", func(op *mockOP) { op.alg = "HS256" }, nil, 401},
		{"no id_token", func(op *mockOP) { op.noIDToken = true }, nil, 401},
		{"userinfo names another subject", func(op *mockOP) { op.sub = "43" }, nil, 401},
	} {
		t.Run(c.name, func(t *testing.T) {
			op := newMockOP(t, "s3cret-value")
			if c.setup != nil {
				c.setup(op)
			}
			h := oidcServer(t, op)
			var edit func([]*http.Cookie) []*http.Cookie
			if c.cookie != nil {
				edit = c.cookie(op)
			}
			w, _ := oauthFlow(t, h, "sso", edit)
			if w.Code != c.want || oauthSession(w) != "" {
				t.Fatalf("got %d (session %v), want %d: %s", w.Code, oauthSession(w) != "", c.want, w.Body)
			}
		})
	}
}

// Several audiences are accepted when this client is the authorized party.
func TestOIDCSeveralAudiencesWithAzp(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	op.claimsEdit = func(c map[string]any) { c["aud"] = []string{"wfx", "other"}; c["azp"] = "wfx" }
	if w, _ := oauthFlow(t, oidcServer(t, op), "sso", nil); w.Code != 303 {
		t.Fatalf("callback: %d %s", w.Code, w.Body)
	}
}

// GitHub issues no id_token: it still gets PKCE, and signs in without one.
func TestGitHubSignInUsesPKCEWithoutAnIDToken(t *testing.T) {
	op := newMockOP(t, "gh-secret")
	op.noIDToken = true
	t.Setenv("TEST_GH_SECRET", "gh-secret")
	h, _ := testServerAuth(t, remoteAddr, config.Auth{Providers: []config.AuthProvider{{
		Name: "gh", Type: "github", ClientID: "wfx", ClientSecretEnv: "TEST_GH_SECRET",
		AuthURL: op.URL() + "/authorize", TokenURL: op.URL() + "/token", UserInfoURL: op.URL() + "/userinfo",
	}}})
	w, loc := oauthFlow(t, h, "gh", nil)
	if q := loc.Query(); q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Has("nonce") {
		t.Fatalf("authorize URL: %s", loc)
	}
	if len(op.gotVerif) < 43 {
		t.Fatalf("no code_verifier sent: %q", op.gotVerif)
	}
	if w.Code != 303 {
		t.Fatalf("callback: %d %s", w.Code, w.Body)
	}
	who := decode(t, withCookie(h, "GET", "/api/whoami", oauthSession(w), nil))
	if who["name"] != "octo@gh" {
		t.Fatalf("whoami: %v", who)
	}
	// Without the verifier cookie GitHub's callback is refused too.
	if w, _ := oauthFlow(t, h, "gh", dropCookie(oauthVerifierCookie)); w.Code != 400 {
		t.Fatalf("no verifier: %d", w.Code)
	}
}
