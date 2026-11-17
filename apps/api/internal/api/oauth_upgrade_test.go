package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// An account with an @ in its name and no password made after the upgrade
// (an admin's local account) is not legacy: no external subject is bound to it.
func TestALocalAccountNamedLikeAnExternalOneIsNeverBound(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	op.userinfo = map[string]any{"preferred_username": "erin"}
	h, st := oidcServerStore(t, op)
	if err := st.CreateUser(context.Background(), &store.User{Name: "erin@sso", Role: RoleSuperadmin}); err != nil {
		t.Fatal(err)
	}
	w, _ := oauthFlow(t, h, "sso", nil)
	if who := whoami(t, h, w); who["name"] == "erin@sso" || who["role"] == RoleSuperadmin {
		t.Fatalf("an external subject got the local account: %v", who)
	}
}

// An LDAP account from before the upgrade binds directly from the LDAP
// username — no legacy bind, no confirmation, roles kept.
func TestALegacyLDAPAccountBindsFromItsUsername(t *testing.T) {
	h, st := testServerAuth(t, "127.0.0.1:8090", config.Auth{Providers: []config.AuthProvider{{
		Name: "corp", Type: "mock-ldap", File: mockDir(t),
	}}})
	markLegacy(t, st, "ana@corp", RoleSuperadmin)
	if w := providerSignIn(h, "corp", "ana", "ana-long-password"); w.Code != 200 {
		t.Fatalf("ldap sign-in: %d %s", w.Code, w.Body)
	}
	u, err := st.UserBySubject(context.Background(), "corp", "ana")
	if err != nil || u.Name != "ana@corp" || u.Role != RoleSuperadmin {
		t.Fatalf("bound = %+v %v", u, err)
	}
	if links, _ := st.PendingLinks(context.Background()); len(links) != 0 {
		t.Fatalf("an LDAP account went pending: %+v", links)
	}
}

// The session cookie's Secure flag follows the public URL's scheme, whatever
// X-Forwarded-Proto says.
func TestTheSessionCookieFollowsThePublicURLScheme(t *testing.T) {
	for _, c := range []struct {
		public, forwarded string
		secure            bool
	}{
		{testPublicURL, "http", true},
		{"http://wfx.local:8090", "https", false},
	} {
		op := newMockOP(t, "s3cret-value")
		t.Setenv("TEST_SSO_SECRET", "s3cret-value")
		h, _ := testServerPublic(t, ssoConfig(op), c.public)
		start := httptest.NewRecorder()
		h.ServeHTTP(start, httptest.NewRequest("GET", "/api/auth/oauth/sso/start", nil))
		loc := start.Header().Get("Location")
		res, err := http.Get(loc)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		u, _ := url.Parse(loc)
		cb := httptest.NewRequest("GET", "/api/auth/oauth/sso/callback?code=good&state="+url.QueryEscape(u.Query().Get("state")), nil)
		cb.Header.Set("X-Forwarded-Proto", c.forwarded)
		for _, ck := range start.Result().Cookies() {
			cb.AddCookie(ck)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, cb)
		if w.Code != 303 {
			t.Fatalf("%s: callback %d %s", c.public, w.Code, w.Body)
		}
		found := false
		for _, ck := range w.Result().Cookies() {
			if ck.Name == sessionCookieName {
				found = true
				if ck.Secure != c.secure {
					t.Errorf("%s with X-Forwarded-Proto %s: session cookie Secure = %v", c.public, c.forwarded, ck.Secure)
				}
			}
		}
		if !found {
			t.Fatalf("%s: no session cookie", c.public)
		}
	}
}
