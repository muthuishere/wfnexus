package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// A server that requires login, with one admin holding a CLI token — the
// state `wfnexus-setup` leaves a compose install in.
func signedInServer(t *testing.T) (http.Handler, *store.Store, string) {
	t.Helper()
	h, st := testServer(t, "0.0.0.0:8090")
	ctx := context.Background()
	u := &store.User{Name: "muthu", DisplayName: "muthu", Role: "admin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	cli := auth.NewToken()
	if _, err := st.IssueUserToken(ctx, u.ID, auth.HashToken(cli), "cli", ""); err != nil {
		t.Fatal(err)
	}
	return h, st, cli
}

// browser is a request carrying only what a browser would: a cookie, and an
// Origin on anything that is not a read.
func browser(h http.Handler, method, path, cookie, origin string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	if body == nil {
		raw = nil
	}
	req := httptest.NewRequest(method, "http://wfx.test"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func sessionFrom(t *testing.T, res *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie set: %d %s", sessionCookieName, res.Code, res.Body)
	return nil
}

func linkCode(t *testing.T, h http.Handler, cli string) string {
	t.Helper()
	res := do(h, "POST", "/api/auth/link", cli, map[string]string{})
	if res.Code != 200 {
		t.Fatalf("link: %d %s", res.Code, res.Body)
	}
	u, err := url.Parse(decode(t, res)["url"].(string))
	if err != nil || u.Path != "/auth/browser" {
		t.Fatalf("link url: %v %v", u, err)
	}
	return u.Query().Get("code")
}

func TestBrowserLinkSignsInOnceThenFails(t *testing.T) {
	h, _, cli := signedInServer(t)
	code := linkCode(t, h, cli)
	if strings.HasPrefix(code, "wfx_") {
		t.Fatal("the link carries a token-shaped value")
	}

	res := browser(h, "GET", "/auth/browser?code="+code, "", "", nil)
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/" {
		t.Fatalf("first use: %d %v", res.Code, res.Header())
	}
	c := sessionFrom(t, res)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Value == "" {
		t.Fatalf("cookie attributes: %+v", c)
	}

	again := browser(h, "GET", "/auth/browser?code="+code, "", "", nil)
	if again.Code != http.StatusGone {
		t.Fatalf("second use of a one-time link: %d", again.Code)
	}
	for _, ck := range again.Result().Cookies() {
		if ck.Name == sessionCookieName && ck.Value != "" {
			t.Fatal("a reused link set a session")
		}
	}
}

func TestBrowserLinkFailsWhenExpired(t *testing.T) {
	h, st, cli := signedInServer(t)
	ctx := context.Background()
	// Mint a link the normal way, then age it past its 60 seconds.
	code := linkCode(t, h, cli)
	dc, err := st.DeviceCodeByHash(ctx, auth.HashToken(code))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.DeleteDeviceCodeByUserCode(ctx, dc.UserCode)
	if err := st.CreateDeviceCode(ctx, auth.HashToken(code), dc.UserCode, browserLinkClient, "", "", time.Now().UTC().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.ApproveDeviceCode(ctx, dc.UserCode, *dc.ApprovedBy); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)

	res := browser(h, "GET", "/auth/browser?code="+code, "", "", nil)
	if res.Code != http.StatusGone {
		t.Fatalf("an expired link signed in: %d", res.Code)
	}
}

func TestSessionCookieIsAcceptedOnReadsAndWrites(t *testing.T) {
	h, _, cli := signedInServer(t)
	c := sessionFrom(t, browser(h, "GET", "/auth/browser?code="+linkCode(t, h, cli), "", "", nil))

	who := browser(h, "GET", "/api/whoami", c.Value, "", nil)
	if who.Code != 200 || decode(t, who)["name"] != "muthu" {
		t.Fatalf("read with the cookie: %d %s", who.Code, who.Body)
	}
	if r := browser(h, "GET", "/api/workflows", "", "", nil); r.Code != 401 {
		t.Fatalf("no cookie, no header, off loopback: %d", r.Code)
	}
	w := browser(h, "POST", "/api/projects", c.Value, "http://wfx.test", map[string]string{"name": "cookie-made"})
	if w.Code == 401 || w.Code == 403 {
		t.Fatalf("a same-origin write with the cookie was refused: %d %s", w.Code, w.Body)
	}
}

func TestCrossOriginWriteWithCookieIsRefused(t *testing.T) {
	h, _, cli := signedInServer(t)
	c := sessionFrom(t, browser(h, "GET", "/auth/browser?code="+linkCode(t, h, cli), "", "", nil))

	for _, origin := range []string{"http://evil.test", ""} {
		res := browser(h, "POST", "/api/projects", c.Value, origin, map[string]string{"name": "csrf"})
		if res.Code != http.StatusForbidden {
			t.Fatalf("origin %q: a cookie write was served: %d %s", origin, res.Code, res.Body)
		}
	}
}

func TestLogoutClearsTheCookieAndRevokesTheToken(t *testing.T) {
	h, _, cli := signedInServer(t)
	c := sessionFrom(t, browser(h, "GET", "/auth/browser?code="+linkCode(t, h, cli), "", "", nil))

	res := browser(h, "DELETE", "/api/tokens/self", c.Value, "http://wfx.test", nil)
	if res.Code != 200 {
		t.Fatalf("logout: %d %s", res.Code, res.Body)
	}
	if cleared := sessionFrom(t, res); cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Fatalf("the cookie was not cleared: %+v", cleared)
	}
	if r := browser(h, "GET", "/api/whoami", c.Value, "", nil); r.Code != 401 {
		t.Fatalf("a logged-out cookie still resolves: %d", r.Code)
	}
	// The CLI's own token is untouched.
	if r := do(h, "GET", "/api/whoami", cli, nil); r.Code != 200 {
		t.Fatalf("the CLI token was revoked too: %d", r.Code)
	}
}

func TestUICardGrantBecomesACookieOnceApprovedFromTheTerminal(t *testing.T) {
	h, _, cli := signedInServer(t)
	start := decode(t, do(h, "POST", "/api/device/code", "", map[string]string{"client_id": uiClient}))
	dev := start["device_code"].(string)

	if e := decode(t, do(h, "POST", "/api/auth/session", "", map[string]string{"device_code": dev}))["error"]; e != "authorization_pending" {
		t.Fatalf("before approval: %v", e)
	}
	// `wfx login --approve <code>` is this request.
	if r := do(h, "POST", "/api/device/verify", cli, map[string]string{"user_code": start["user_code"].(string)}); r.Code != 200 {
		t.Fatalf("approve: %d %s", r.Code, r.Body)
	}
	// A browser's grant is never turned into a bearer value.
	if e := decode(t, do(h, "POST", "/api/device/token", "", map[string]string{"device_code": dev}))["error"]; e != "invalid_grant" {
		t.Fatalf("/device/token redeemed a browser grant: %v", e)
	}
	res := do(h, "POST", "/api/auth/session", "", map[string]string{"device_code": dev})
	c := sessionFrom(t, res)
	if r := browser(h, "GET", "/api/whoami", c.Value, "", nil); r.Code != 200 {
		t.Fatalf("the session does not resolve: %d", r.Code)
	}
	if r := do(h, "POST", "/api/auth/session", "", map[string]string{"device_code": dev}); r.Code != 400 {
		t.Fatalf("the grant was redeemed twice: %d", r.Code)
	}
}
