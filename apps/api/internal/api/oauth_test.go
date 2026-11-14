package api

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

func ssoConfig(op *mockOP) config.Auth {
	return config.Auth{Providers: []config.AuthProvider{{
		Name: "sso", Type: "oidc", Issuer: op.URL(), ClientID: "wfx", ClientSecretEnv: "TEST_SSO_SECRET",
		GroupRoles: map[string]string{"admins": "superadmin"},
	}}}
}

func oidcServer(t *testing.T, op *mockOP) http.Handler {
	t.Helper()
	h, _ := oidcServerStore(t, op)
	return h
}

func oidcServerStore(t *testing.T, op *mockOP) (http.Handler, *store.Store) {
	t.Helper()
	t.Setenv("TEST_SSO_SECRET", "s3cret-value")
	return testServerPublic(t, ssoConfig(op), testPublicURL)
}

func whoami(t *testing.T, h http.Handler, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	return decode(t, withCookie(h, "GET", "/api/whoami", oauthSession(w), nil))
}

// The authorize redirect carries a PKCE S256 challenge, a nonce and the
// callback built from the configured public URL (the start request's Host is
// forged); the token request carries the verifier; a valid id_token signs the
// person in; the one signed sign-in cookie is a __Host- cookie, and is cleared.
func TestOIDCSignInSendsPKCEAndNonceAndClearsItsCookie(t *testing.T) {
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
			if want := testPublicURL + "/api/auth/oauth/sso/callback"; op.gotRedir != want || q.Get("redirect_uri") != want {
				t.Fatalf("redirect_uri = %q, want %q", op.gotRedir, want)
			}
			if len(op.gotVerif) < 43 {
				t.Fatalf("token request verifier = %q", op.gotVerif)
			}
			if w.Code != 303 {
				t.Fatalf("callback: %d %s", w.Code, w.Body)
			}
			cleared := false
			for _, c := range w.Result().Cookies() {
				cleared = cleared || (c.Name == oauthCookieName && c.MaxAge < 0 && c.Path == "/" && c.Secure)
			}
			if !cleared {
				t.Error("the sign-in cookie was not cleared after the callback")
			}
			if who := whoami(t, h, w); who["name"] != "bob@sso" || who["role"] != RoleSuperadmin {
				t.Fatalf("whoami: %v", who)
			}
		})
	}
}

// The start sets one cookie: __Host- over https (Path=/, Secure, HttpOnly).
func TestTheSignInCookieIsHostPrefixedAndSecureOverHTTPS(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	h := oidcServer(t, op)
	start := httptest.NewRecorder()
	h.ServeHTTP(start, httptest.NewRequest("GET", "/api/auth/oauth/sso/start", nil))
	cs := start.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != oauthCookieName || cs[0].Path != "/" || !cs[0].Secure || !cs[0].HttpOnly || cs[0].Domain != "" {
		t.Fatalf("cookies = %+v", cs)
	}
}

// Each of these is refused, nobody is signed in, and no provider text reaches
// the browser.
func TestOIDCSignInRefusals(t *testing.T) {
	other := []byte("another-server-key-another-server-key")
	for _, c := range []struct {
		name   string
		setup  func(op *mockOP)
		cookie func([]*http.Cookie) []*http.Cookie
		want   int
	}{
		{"no cookie", nil, withoutCookies, 400},
		{"tampered cookie", nil, replaceTicket("eyJwIjoic3NvIn0.AAAA"), 400},
		{"cookie signed by another server", nil, reseal(other, func(*oauthTicket) {}), 400},
		{"expired cookie", nil, reseal(fixedJWTKey, func(t *oauthTicket) { t.Expires = time.Now().Add(-time.Minute).Unix() }), 400},
		{"cookie for another provider", nil, reseal(fixedJWTKey, func(t *oauthTicket) { t.Provider = "elsewhere" }), 400},
		{"wrong verifier", func(op *mockOP) { op.badPKCE = true }, nil, 401},
		{"bad signature", func(op *mockOP) { op.badSig = true }, nil, 401},
		{"wrong issuer", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["iss"] = "https://elsewhere.example" } }, nil, 401},
		{"wrong audience", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["aud"] = "someone-else" } }, nil, 401},
		{"several audiences, no azp", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["aud"] = []string{"wfx", "other"} } }, nil, 401},
		{"azp is another client", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["azp"] = "other" } }, nil, 401},
		{"no exp", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { delete(c, "exp") } }, nil, 401},
		{"no iat", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { delete(c, "iat") } }, nil, 401},
		{"expired", func(op *mockOP) {
			op.claimsEdit = func(c map[string]any) { c["exp"] = time.Now().Add(-10 * time.Minute).Unix() }
		}, nil, 401},
		{"issued in the future", func(op *mockOP) {
			op.claimsEdit = func(c map[string]any) { c["iat"] = time.Now().Add(10 * time.Minute).Unix() }
		}, nil, 401},
		{"wrong nonce", func(op *mockOP) { op.claimsEdit = func(c map[string]any) { c["nonce"] = "not-this-sign-in" } }, nil, 401},
		{"alg none", func(op *mockOP) { op.alg = "none" }, nil, 401},
		{"alg HS256", func(op *mockOP) { op.alg = "HS256" }, nil, 401},
		{"no id_token", func(op *mockOP) { op.noIDToken = true }, nil, 401},
		{"userinfo names another subject", func(op *mockOP) { op.uiSub = "43" }, nil, 401},
	} {
		t.Run(c.name, func(t *testing.T) {
			op := newMockOP(t, "s3cret-value")
			if c.setup != nil {
				c.setup(op)
			}
			h := oidcServer(t, op)
			w, _ := oauthFlow(t, h, "sso", c.cookie)
			if w.Code != c.want || oauthSession(w) != "" {
				t.Fatalf("got %d (session %v), want %d: %s", w.Code, oauthSession(w) != "", c.want, w.Body)
			}
			if strings.Contains(w.Body.String(), "provider-detail-text") || strings.Contains(w.Body.String(), "oidc:") {
				t.Fatalf("provider text reached the browser: %s", w.Body)
			}
		})
	}
}

// Several audiences are accepted when this client is the authorized party,
// and so is a single audience with azp equal to the client.
func TestOIDCAzpEqualToTheClientIsAccepted(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"several audiences": func(c map[string]any) { c["aud"] = []string{"wfx", "other"}; c["azp"] = "wfx" },
		"one audience":      func(c map[string]any) { c["azp"] = "wfx" },
	} {
		t.Run(name, func(t *testing.T) {
			op := newMockOP(t, "s3cret-value")
			op.claimsEdit = edit
			if w, _ := oauthFlow(t, oidcServer(t, op), "sso", nil); w.Code != 303 {
				t.Fatalf("callback: %d %s", w.Code, w.Body)
			}
		})
	}
}

// Discovery naming another issuer than the configured one stops the sign-in
// before the browser is sent anywhere, and says nothing of why.
func TestDiscoveryNamingAnotherIssuerIsRefused(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	op.discIssuer = "https://impostor.example"
	h := oidcServer(t, op)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/auth/oauth/sso/start", nil))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "impostor") {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
}

// Without a public URL no redirect provider is enabled: its callback address
// would otherwise come from the request.
func TestRedirectProvidersNeedThePublicURL(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	t.Setenv("TEST_SSO_SECRET", "s3cret-value")
	h, _ := testServerPublic(t, ssoConfig(op), "")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/auth/oauth/sso/start", nil))
	if w.Code != 404 {
		t.Fatalf("start without a public URL = %d", w.Code)
	}
}

// The account is keyed on the verified subject. Sub 42 "bob" is a superadmin
// through his group; sub 99 calling itself bob gets an account of its own,
// with nothing of bob's.
func TestAnotherSubjectWithTheSameNameDoesNotGetTheAccount(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	h := oidcServer(t, op)
	w, _ := oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); who["name"] != "bob@sso" || who["role"] != RoleSuperadmin {
		t.Fatalf("bob: %v", who)
	}
	op.sub = "99"
	op.userinfo = map[string]any{"preferred_username": "bob"}
	w, _ = oauthFlow(t, h, "sso", nil)
	if w.Code != 303 {
		t.Fatalf("callback: %d %s", w.Code, w.Body)
	}
	if who := whoami(t, h, w); who["name"] == "bob@sso" || who["role"] == RoleSuperadmin {
		t.Fatalf("sub 99 got bob's account: %v", who)
	}
}

// Roles mapped from groups follow the groups at every sign-in: taken away
// when the group is gone.
func TestMappedRolesAreResyncedAtEverySignIn(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	h := oidcServer(t, op)
	w, _ := oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); who["role"] != RoleSuperadmin {
		t.Fatalf("first: %v", who)
	}
	op.userinfo = map[string]any{"preferred_username": "bob", "groups": []string{}}
	w, _ = oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); who["name"] != "bob@sso" || who["role"] == RoleSuperadmin {
		t.Fatalf("after leaving the group: %v", who)
	}
}

// An email the provider has not verified does not become the account name.
func TestAnUnverifiedEmailIsNotUsedAsTheName(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	op.userinfo = map[string]any{"email": "boss@corp.example", "email_verified": false}
	t.Setenv("TEST_SSO_SECRET", "s3cret-value")
	a := ssoConfig(op)
	a.Providers[0].UsernameClaim = "email"
	h, _ := testServerPublic(t, a, testPublicURL)
	w, _ := oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); strings.Contains(who["name"].(string), "boss@corp.example") {
		t.Fatalf("an unverified email named the account: %v", who)
	}
	op.userinfo = map[string]any{"email": "boss@corp.example", "email_verified": true}
	op.sub = "77"
	w, _ = oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); who["name"] != "boss@corp.example@sso" {
		t.Fatalf("a verified email: %v", who)
	}
}

func markLegacy(t *testing.T, st *store.Store, name, role string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{Name: name, Role: role}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE users SET link_state='legacy' WHERE name=$1`, name); err != nil {
		t.Fatal(err)
	}
}

// An account made by an external sign-in before the upgrade, holding no
// roles, is bound to the first subject that signs in with its name, with an
// audit line; a different subject later gets an account of its own.
func TestALegacyAccountWithoutRolesIsBoundOnceWithAnAuditLine(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	op.userinfo = map[string]any{"preferred_username": "dana"}
	h, st := oidcServerStore(t, op)
	markLegacy(t, st, "dana@sso", "")
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	w, _ := oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); who["name"] != "dana@sso" {
		t.Fatalf("legacy bind: %d %v", w.Code, who)
	}
	if !strings.Contains(logs.String(), "AUDIT account dana@sso linked to sso subject 42 on first sign-in after upgrade") {
		t.Fatalf("no audit line: %s", logs.String())
	}
	op.sub = "99"
	w, _ = oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); who["name"] == "dana@sso" {
		t.Fatalf("a second subject got the bound account: %v", who)
	}
}

// A legacy account that holds roles is bound but signs in only after an admin
// confirms the link; then it signs in with its roles.
func TestALegacyAccountWithRolesWaitsForConfirmation(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	op.userinfo = map[string]any{"preferred_username": "carol"}
	// No group mapping here: the role is the account's own, not the provider's.
	t.Setenv("TEST_SSO_SECRET", "s3cret-value")
	a := ssoConfig(op)
	a.Providers[0].GroupRoles = nil
	h, st := testServerPublic(t, a, testPublicURL)
	markLegacy(t, st, "carol@sso", RoleSuperadmin)
	w, _ := oauthFlow(t, h, "sso", nil)
	if w.Code != http.StatusForbidden || oauthSession(w) != "" || !strings.Contains(w.Body.String(), "pending confirmation") {
		t.Fatalf("before confirmation: %d %s", w.Code, w.Body)
	}
	links, _ := st.PendingLinks(context.Background())
	if len(links) != 1 || links[0].Name != "carol@sso" || links[0].Subject != "42" {
		t.Fatalf("pending = %+v", links)
	}
	if err := st.ConfirmLink(context.Background(), "carol@sso"); err != nil {
		t.Fatal(err)
	}
	w, _ = oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); who["name"] != "carol@sso" || who["role"] != RoleSuperadmin {
		t.Fatalf("after confirmation: %d %v", w.Code, who)
	}
}

// GitHub issues no id_token: it still gets PKCE, and its accounts are keyed
// on the numeric user id, so a renamed login keeps its account and a new
// person reusing an old login does not get it.
func TestGitHubSignInUsesPKCEAndKeysOnTheUserID(t *testing.T) {
	op := newMockOP(t, "gh-secret")
	op.noIDToken = true
	op.userinfo = map[string]any{"id": 1001, "login": "octo"}
	t.Setenv("TEST_GH_SECRET", "gh-secret")
	h, _ := testServerPublic(t, config.Auth{Providers: []config.AuthProvider{{
		Name: "gh", Type: "github", ClientID: "wfx", ClientSecretEnv: "TEST_GH_SECRET",
		AuthURL: op.URL() + "/authorize", TokenURL: op.URL() + "/token", UserInfoURL: op.URL() + "/userinfo",
	}}}, testPublicURL)
	gh := func() map[string]any {
		t.Helper()
		w, loc := oauthFlow(t, h, "gh", nil)
		if q := loc.Query(); q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Has("nonce") {
			t.Fatalf("authorize URL: %s", loc)
		}
		if w.Code != 303 {
			t.Fatalf("callback: %d %s", w.Code, w.Body)
		}
		return whoami(t, h, w)
	}
	first := gh()
	if first["name"] != "octo@gh" || len(op.gotVerif) < 43 {
		t.Fatalf("whoami: %v (verifier %q)", first, op.gotVerif)
	}
	op.userinfo = map[string]any{"id": 1001, "login": "octo-renamed"}
	if again := gh(); again["name"] != "octo@gh" {
		t.Fatalf("a renamed login lost its account: %v", again)
	}
	op.userinfo = map[string]any{"id": 2002, "login": "octo"}
	if other := gh(); other["name"] == "octo@gh" {
		t.Fatalf("another GitHub user with the old login got the account: %v", other)
	}
	if w, _ := oauthFlow(t, h, "gh", withoutCookies); w.Code != 400 {
		t.Fatalf("no cookie: %d", w.Code)
	}
}
