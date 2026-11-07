package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// ADR 0025: username and password sign-in.

const remoteAddr = "10.0.0.5:8090"

func pwUser(t *testing.T, st *store.Store, name, role, pw string, mustChange bool) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{Name: name, Role: role}); err != nil {
		t.Fatal(err)
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(ctx, name, h, mustChange); err != nil {
		t.Fatal(err)
	}
}

// signIn posts the form from a given source address.
func signIn(h http.Handler, ip, user, pw string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	req := httptest.NewRequest("POST", "/api/auth/password", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func pwSession(t *testing.T, res *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("no session cookie in %v", res.Header())
	return ""
}

// withCookie sends a same-origin browser request carrying the session cookie.
func withCookie(h http.Handler, method, path, cookie string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	if body == nil {
		raw = nil
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestPasswordSignInSetsTheSessionCookie(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "ana", "user", "a-long-password-1", false)

	res := signIn(h, "192.0.2.1", "ana", "a-long-password-1")
	if res.Code != 200 {
		t.Fatalf("sign-in: %d %s", res.Code, res.Body)
	}
	c := pwSession(t, res)
	// The cookie is an ordinary user token: it resolves through the one middleware.
	who := withCookie(h, "GET", "/api/whoami", c, nil)
	if !strings.Contains(who.Body.String(), `"name":"ana"`) {
		t.Fatalf("whoami with the cookie: %s", who.Body)
	}
	// Signing out revokes it like every other session.
	if out := withCookie(h, "DELETE", "/api/tokens/self", c, nil); out.Code != 200 {
		t.Fatalf("sign out: %d %s", out.Code, out.Body)
	}
	if again := withCookie(h, "GET", "/api/workflows", c, nil); again.Code != 401 {
		t.Fatalf("a revoked password session still works: %d", again.Code)
	}
}

func TestUnknownUserAndWrongPasswordLookIdentical(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "ana", "user", "a-long-password-1", false)
	pwUser(t, st, "gone", "user", "a-long-password-2", false)
	if _, err := st.UpdateUser(context.Background(), "gone", "", "user", "", true); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(context.Background(), &store.User{Name: "nopw", Role: "user"}); err != nil {
		t.Fatal(err)
	}

	wrong := signIn(h, "192.0.2.1", "ana", "not-the-password")
	cases := map[string]*httptest.ResponseRecorder{
		"unknown user":      signIn(h, "192.0.2.2", "nobody", "not-the-password"),
		"disabled user":     signIn(h, "192.0.2.3", "gone", "a-long-password-2"),
		"user, no password": signIn(h, "192.0.2.4", "nopw", ""),
	}
	if wrong.Code != 401 {
		t.Fatalf("wrong password: %d", wrong.Code)
	}
	for name, res := range cases {
		if res.Code != wrong.Code || res.Body.String() != wrong.Body.String() {
			t.Fatalf("%s answered %d %q; a wrong password answered %d %q",
				name, res.Code, res.Body, wrong.Code, wrong.Body)
		}
		if len(res.Result().Cookies()) != 0 {
			t.Fatalf("%s set a cookie", name)
		}
	}
}

func TestPasswordAttemptsAreLimitedPerUsernameAndPerIP(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "ana", "user", "a-long-password-1", false)

	// Per username: many addresses against one account.
	for i := 0; i < verifyPerMinute; i++ {
		signIn(h, "198.51.100."+string(rune('1'+i)), "ana", "wrong-password-x")
	}
	if res := signIn(h, "198.51.100.99", "ana", "a-long-password-1"); res.Code != http.StatusTooManyRequests {
		t.Fatalf("per-username limit: the right password from a fresh address got %d", res.Code)
	}
	// Username is matched case-insensitively for the limit.
	if res := signIn(h, "198.51.100.98", "ANA", "x"); res.Code != http.StatusTooManyRequests {
		t.Fatalf("per-username limit bypassed by case: %d", res.Code)
	}

	// Per IP: one address against many accounts.
	for i := 0; i < verifyPerMinute; i++ {
		signIn(h, "203.0.113.7", "user"+string(rune('a'+i)), "wrong-password-x")
	}
	if res := signIn(h, "203.0.113.7", "someone-new", "wrong-password-x"); res.Code != http.StatusTooManyRequests {
		t.Fatalf("per-IP limit: %d", res.Code)
	}
}

func TestMustChangeBlocksEverythingButChangingThePassword(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "admin", "superadmin", "one-time-password-1", true)

	res := signIn(h, "192.0.2.1", "admin", "one-time-password-1")
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"mustChange":true`) {
		t.Fatalf("sign-in: %d %s", res.Code, res.Body)
	}
	c := pwSession(t, res)
	if who := withCookie(h, "GET", "/api/whoami", c, nil); !strings.Contains(who.Body.String(), `"mustChange":true`) {
		t.Fatalf("whoami does not say mustChange: %s", who.Body)
	}
	if got := withCookie(h, "GET", "/api/users", c, nil); got.Code != 403 {
		t.Fatalf("a must-change admin listed users: %d", got.Code)
	}
	// Too short.
	if got := withCookie(h, "POST", "/api/auth/password/change", c,
		map[string]string{"current": "one-time-password-1", "new": "short"}); got.Code != 400 {
		t.Fatalf("a short new password: %d %s", got.Code, got.Body)
	}
	// Wrong current.
	if got := withCookie(h, "POST", "/api/auth/password/change", c,
		map[string]string{"current": "nope-nope-nope", "new": "a-brand-new-password"}); got.Code != 403 {
		t.Fatalf("a wrong current password: %d %s", got.Code, got.Body)
	}
	if got := withCookie(h, "POST", "/api/auth/password/change", c,
		map[string]string{"current": "one-time-password-1", "new": "a-brand-new-password"}); got.Code != 200 {
		t.Fatalf("change: %d %s", got.Code, got.Body)
	}
	if got := withCookie(h, "GET", "/api/users", c, nil); got.Code != 200 {
		t.Fatalf("after the change, still blocked: %d %s", got.Code, got.Body)
	}
	if res := signIn(h, "192.0.2.9", "admin", "one-time-password-1"); res.Code != 401 {
		t.Fatalf("the one-time password still works: %d", res.Code)
	}
	if res := signIn(h, "192.0.2.9", "admin", "a-brand-new-password"); res.Code != 200 ||
		!strings.Contains(res.Body.String(), `"mustChange":false`) {
		t.Fatalf("the new password: %d %s", res.Code, res.Body)
	}
}

func TestAdminResetPasswordIsOneTimeAndRequiresAdmin(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "boss", "superadmin", "boss-password-123", false)
	pwUser(t, st, "ana", "user", "ana-password-123", false)
	boss := pwSession(t, signIn(h, "192.0.2.1", "boss", "boss-password-123"))
	ana := pwSession(t, signIn(h, "192.0.2.2", "ana", "ana-password-123"))

	if got := withCookie(h, "POST", "/api/users/boss/password", ana, nil); got.Code != 403 {
		t.Fatalf("a viewer reset an admin's password: %d", got.Code)
	}
	if got := withCookie(h, "POST", "/api/users/ana/password", boss, map[string]string{"password": "short"}); got.Code != 400 {
		t.Fatalf("a short reset password: %d", got.Code)
	}
	got := withCookie(h, "POST", "/api/users/ana/password", boss, nil)
	var out struct {
		Password   string `json:"password"`
		MustChange bool   `json:"mustChange"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &out)
	if got.Code != 200 || len(out.Password) < auth.MinPasswordLen || !out.MustChange {
		t.Fatalf("reset: %d %s", got.Code, got.Body)
	}
	if res := signIn(h, "192.0.2.3", "ana", "ana-password-123"); res.Code != 401 {
		t.Fatalf("the old password survived a reset: %d", res.Code)
	}
	res := signIn(h, "192.0.2.3", "ana", out.Password)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"mustChange":true`) {
		t.Fatalf("the reset password: %d %s", res.Code, res.Body)
	}
	// The hash never appears in the users list.
	list := withCookie(h, "GET", "/api/users", boss, nil)
	if strings.Contains(list.Body.String(), "argon2") || !strings.Contains(list.Body.String(), `"hasPassword":true`) {
		t.Fatalf("users list: %s", list.Body)
	}
}

func TestPasswordSignInIsNotOfferedOnLoopback(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")
	pwUser(t, st, "ana", "user", "a-long-password-1", false)
	if res := signIn(h, "127.0.0.1", "ana", "a-long-password-1"); res.Code != 404 {
		t.Fatalf("loopback password sign-in: %d", res.Code)
	}
	if res := do(h, "GET", "/api/whoami", "", nil); !strings.Contains(res.Body.String(), `"authRequired":false`) {
		t.Fatalf("whoami on loopback: %s", res.Body)
	}
}

func TestPasswordsAreNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "ana", "user", "secret-password-xyz", false)
	signIn(h, "192.0.2.1", "ana", "secret-password-xyz")
	signIn(h, "192.0.2.1", "ana", "wrong-secret-abc")
	if strings.Contains(buf.String(), "secret-password-xyz") || strings.Contains(buf.String(), "wrong-secret-abc") {
		t.Fatalf("a password reached the log:\n%s", buf.String())
	}
}
