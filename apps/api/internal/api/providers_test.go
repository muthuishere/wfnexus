package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// ADR 0028: login providers authenticate; roles stay ours.

func testServerAuth(t *testing.T, addr string, a config.Auth) (http.Handler, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	eng := engine.New(config.Config{Addr: addr, Auth: a}, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	return New(eng, st, nil, addr, "", nil), st
}

func mockDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "directory.yaml")
	yaml := "users:\n  - username: ana\n    password: ana-long-password\n    groups: [ops]\n  - username: bo\n    password: bo-long-password\n"
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func providerSignIn(h http.Handler, provider, user, pw string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]string{"username": user, "password": pw, "provider": provider, "client": "cli"})
	req := httptest.NewRequest("POST", "/api/auth/password", bytes.NewReader(raw))
	req.RemoteAddr = "203.0.113.9:4000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestMockLDAPSignInMapsGroupsAndNothingElse(t *testing.T) {
	h, st := testServerAuth(t, remoteAddr, config.Auth{Providers: []config.AuthProvider{{
		Name: "corp", Type: "mock-ldap", File: mockDir(t), Force: true,
		GroupRoles: map[string]string{"ops": "workflow_runner:w1"},
	}}})
	// A LOCAL user of the same name is not who the directory signs in.
	pwUser(t, st, "ana", RoleSuperadmin, "local-long-password", false)

	w := providerSignIn(h, "corp", "ana", "ana-long-password")
	if w.Code != 200 {
		t.Fatalf("mock-ldap sign-in: %d %s", w.Code, w.Body)
	}
	access := decode(t, w)["access_token"].(string)
	who := decode(t, do(h, "GET", "/api/whoami", access, nil))
	if who["name"] != "ana@corp" || who["role"] != "" {
		t.Fatalf("whoami: %v", who)
	}
	if gs, _ := json.Marshal(who["grants"]); !strings.Contains(string(gs), `"workflow_runner"`) || !strings.Contains(string(gs), `"w1"`) {
		t.Fatalf("group ops did not map to workflow_runner on w1: %s", gs)
	}
	// No group, no default: no roles at all — reads only.
	w = providerSignIn(h, "corp", "bo", "bo-long-password")
	bo := decode(t, w)["access_token"].(string)
	if g := do(h, "POST", "/api/workflows/w1/runs", bo, map[string]any{}); g.Code != 403 {
		t.Fatalf("a new external user with no roles could run: %d", g.Code)
	}
	if w := providerSignIn(h, "corp", "ana", "wrong-password-here"); w.Code != 401 {
		t.Fatalf("a wrong directory password: %d", w.Code)
	}
	if w := providerSignIn(h, "nope", "ana", "ana-long-password"); w.Code != 401 {
		t.Fatalf("an unknown provider: %d", w.Code)
	}
}

func TestMockLDAPIsRefusedOffLoopbackUnlessForced(t *testing.T) {
	h, _ := testServerAuth(t, remoteAddr, config.Auth{Providers: []config.AuthProvider{{
		Name: "corp", Type: "mock-ldap", File: mockDir(t),
	}}})
	if w := providerSignIn(h, "corp", "ana", "ana-long-password"); w.Code != 401 {
		t.Fatalf("mock-ldap off loopback without force signed someone in: %d", w.Code)
	}
	if body := do(h, "GET", "/api/auth/providers", "", nil).Body.String(); strings.Contains(body, "corp") {
		t.Fatalf("mock-ldap listed off loopback: %s", body)
	}
	// On loopback it is enabled without force.
	h2, _ := testServerAuth(t, "127.0.0.1:8090", config.Auth{Providers: []config.AuthProvider{{
		Name: "corp", Type: "mock-ldap", File: mockDir(t),
	}}})
	if w := providerSignIn(h2, "corp", "ana", "ana-long-password"); w.Code != 200 {
		t.Fatalf("mock-ldap on loopback: %d %s", w.Code, w.Body)
	}
}

// fakeIssuer is a minimal OpenID Provider: discovery, token, userinfo.
func fakeIssuer(t *testing.T, secret string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize",
				"token_endpoint": srv.URL + "/token", "userinfo_endpoint": srv.URL + "/userinfo",
			})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != secret || r.Form.Get("client_id") != "wfx" {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at-123", "token_type": "Bearer"})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer at-123" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "42", "preferred_username": "bob", "groups": []string{"admins"}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOIDCSignInThroughAFakeIssuer(t *testing.T) {
	t.Setenv("TEST_SSO_SECRET", "s3cret-value")
	iss := fakeIssuer(t, "s3cret-value")
	h, _ := testServerAuth(t, remoteAddr, config.Auth{Providers: []config.AuthProvider{{
		Name: "sso", Type: "oidc", Issuer: iss.URL, ClientID: "wfx", ClientSecretEnv: "TEST_SSO_SECRET",
		GroupRoles: map[string]string{"admins": "superadmin"},
	}}})

	start := httptest.NewRecorder()
	h.ServeHTTP(start, httptest.NewRequest("GET", "/api/auth/oauth/sso/start", nil))
	if start.Code != 302 {
		t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	loc, _ := url.Parse(start.Header().Get("Location"))
	state := loc.Query().Get("state")
	if !strings.HasPrefix(loc.String(), iss.URL+"/authorize") || state == "" || loc.Query().Get("client_id") != "wfx" {
		t.Fatalf("authorize redirect: %s", loc)
	}
	var stateCookie *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == oauthStateCookie {
			stateCookie = c
		}
	}

	callback := func(code, st string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/auth/oauth/sso/callback?code="+code+"&state="+st, nil)
		req.AddCookie(stateCookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := callback("good", "forged-state"); w.Code != 400 {
		t.Fatalf("a forged state: %d", w.Code)
	}
	if w := callback("bad", state); w.Code != 401 {
		t.Fatalf("a bad code: %d %s", w.Code, w.Body)
	}
	w := callback("good", state)
	if w.Code != 303 {
		t.Fatalf("callback: %d %s", w.Code, w.Body)
	}
	var session string
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			session = c.Value
		}
	}
	who := decode(t, withCookie(h, "GET", "/api/whoami", session, nil))
	if who["name"] != "bob@sso" || who["role"] != RoleSuperadmin {
		t.Fatalf("whoami after OIDC: %v", who)
	}
}
