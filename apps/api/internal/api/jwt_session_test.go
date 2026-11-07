package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// ADR 0028: one login, one JWT, four roles.

var cliIP int

func cliSignIn(t *testing.T, h http.Handler, user, pw string) (access, refresh string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"username": user, "password": pw, "client": "cli"})
	req := httptest.NewRequest("POST", "/api/auth/password", bytes.NewReader(raw))
	cliIP++
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", cliIP%250)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("cli sign-in: %d %s", w.Code, w.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	access, _ = out["access_token"].(string)
	refresh, _ = out["refresh_token"].(string)
	if strings.Count(access, ".") != 2 || !strings.HasPrefix(refresh, "wfx_") || out["expires_in"] != float64(900) {
		t.Fatalf("token response: %v", out)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("a CLI sign-in set cookies")
	}
	return access, refresh
}

func postRefresh(h http.Handler, refresh string) *httptest.ResponseRecorder {
	return do(h, "POST", "/api/auth/refresh", "", map[string]string{"refresh_token": refresh})
}

func moveClock(t *testing.T, d time.Duration) {
	t.Helper()
	jwtNow = func() time.Time { return time.Now().Add(d) }
	t.Cleanup(func() { jwtNow = time.Now })
}

func TestJWTWorksExpiresAndRefreshes(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "ana", "user", "a-long-password-1", false)
	access, refresh := cliSignIn(t, h, "ana", "a-long-password-1")

	if w := do(h, "GET", "/api/whoami", access, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"ana"`) {
		t.Fatalf("whoami with the JWT: %d %s", w.Code, w.Body)
	}
	// A refresh token is not a bearer.
	if w := do(h, "GET", "/api/workflows", refresh, nil); w.Code != 401 {
		t.Fatalf("a refresh token was accepted as a bearer: %d", w.Code)
	}
	// A tampered JWT is refused.
	if w := do(h, "GET", "/api/workflows", access[:len(access)-2]+"xx", nil); w.Code != 401 {
		t.Fatalf("a tampered JWT: %d", w.Code)
	}

	// Sixteen minutes on, the access token has expired...
	moveClock(t, 16*time.Minute)
	if w := do(h, "GET", "/api/workflows", access, nil); w.Code != 401 {
		t.Fatalf("an expired JWT was accepted: %d", w.Code)
	}
	// ...and the refresh token buys a new one.
	w := postRefresh(h, refresh)
	if w.Code != 200 {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	fresh := decode(t, w)["access_token"].(string)
	if w := do(h, "GET", "/api/workflows", fresh, nil); w.Code != 200 {
		t.Fatalf("the refreshed JWT: %d %s", w.Code, w.Body)
	}
	if w := postRefresh(h, "wfx_not-a-real-one"); w.Code != 401 {
		t.Fatalf("an unknown refresh token: %d", w.Code)
	}
}

func TestLogoutRevokesTheJWTAndTheRefreshToken(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "ana", "user", "a-long-password-1", false)
	access, refresh := cliSignIn(t, h, "ana", "a-long-password-1")
	if w := do(h, "DELETE", "/api/tokens/self", access, nil); w.Code != 200 {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	// Not "in fifteen minutes": now.
	if w := do(h, "GET", "/api/workflows", access, nil); w.Code != 401 {
		t.Fatalf("a revoked session's JWT still works: %d", w.Code)
	}
	if w := postRefresh(h, refresh); w.Code != 401 {
		t.Fatalf("a revoked refresh token still refreshes: %d", w.Code)
	}

	// Disabling the user ends their other sessions too.
	access2, refresh2 := cliSignIn(t, h, "ana", "a-long-password-1")
	if _, err := st.UpdateUser(context.Background(), "ana", "", "user", "", true); err != nil {
		t.Fatal(err)
	}
	if w := do(h, "GET", "/api/workflows", access2, nil); w.Code != 401 {
		t.Fatalf("a disabled user's JWT: %d", w.Code)
	}
	if w := postRefresh(h, refresh2); w.Code != 401 {
		t.Fatalf("a disabled user's refresh: %d", w.Code)
	}
}

func TestBrowserCookieRefreshesItself(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	pwUser(t, st, "ana", "user", "a-long-password-1", false)
	res := signIn(h, "192.0.2.1", "ana", "a-long-password-1")
	var session, refresh string
	for _, c := range res.Result().Cookies() {
		switch c.Name {
		case sessionCookieName:
			session = c.Value
		case refreshCookieName:
			refresh = c.Value
			if !c.HttpOnly {
				t.Fatal("the refresh cookie is readable by page script")
			}
		}
	}
	if strings.Count(session, ".") != 2 || !strings.HasPrefix(refresh, "wfx_") {
		t.Fatalf("cookies: session=%q refresh=%q", session, refresh)
	}
	if strings.Contains(res.Body.String(), refresh) {
		t.Fatal("the browser sign-in put the refresh value in the body")
	}

	moveClock(t, time.Hour)
	req := httptest.NewRequest("GET", "/api/workflows", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: refresh})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("an expired cookie with a live refresh cookie: %d %s", w.Code, w.Body)
	}
	renewed := false
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" && c.Value != session {
			renewed = true
		}
	}
	if !renewed {
		t.Fatal("the expired session cookie was not replaced")
	}
	// Without the refresh cookie, the expired JWT alone is refused.
	if w := withCookie(h, "GET", "/api/workflows", session, nil); w.Code != 401 {
		t.Fatalf("an expired session cookie alone: %d", w.Code)
	}
}

// Each role, allowed and denied (ADR 0028).
func TestFourRoles(t *testing.T) {
	h, st := testServer(t, remoteAddr)
	ctx := context.Background()
	tok := map[string]string{}
	for name, role := range map[string]string{
		"root": "superadmin", "ed": "editor", "own": "user", "med": "user", "plain": "user", "other": "user",
	} {
		pwUser(t, st, name, role, "a-long-password-1", false)
		tok[name], _ = cliSignIn(t, h, name, "a-long-password-1")
	}
	setMember := func(user, role string) {
		u, _ := st.UserByName(ctx, user)
		if err := st.SetWorkflowMember(ctx, "w1", u.ID, role); err != nil {
			t.Fatal(err)
		}
	}
	setMember("own", "owner")
	setMember("med", "editor")

	run, err := st.CreateRun(ctx, "local", "w1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTriggeredBy(ctx, run.ID, "other"); err != nil {
		t.Fatal(err)
	}
	mine, _ := st.CreateRun(ctx, "local", "w2", json.RawMessage(`{}`))
	_ = st.SetTriggeredBy(ctx, mine.ID, "plain")

	denied := func(w *httptest.ResponseRecorder) bool { return w.Code == 401 || w.Code == 403 }
	type action struct {
		name string
		call func(tok string) *httptest.ResponseRecorder
		gone func(w *httptest.ResponseRecorder) bool // what "refused" looks like
	}
	hidden := func(w *httptest.ResponseRecorder) bool { return w.Code == 404 || denied(w) }
	actions := []action{
		{"edit", func(t string) *httptest.ResponseRecorder {
			return do(h, "PUT", "/api/workflows/w1", t, map[string]any{})
		}, denied},
		{"run", func(t string) *httptest.ResponseRecorder {
			return do(h, "POST", "/api/workflows/w1/runs", t, map[string]any{})
		}, denied},
		{"publish", func(t string) *httptest.ResponseRecorder {
			return do(h, "POST", "/api/workflows/w1/expose/hook", t, nil)
		}, denied},
		{"members", func(t string) *httptest.ResponseRecorder {
			return do(h, "PUT", "/api/workflows/w1/members/other", t, map[string]string{"role": "editor"})
		}, denied},
		{"see-others-run", func(t string) *httptest.ResponseRecorder {
			return do(h, "GET", "/api/runs/"+run.ID.String(), t, nil)
		}, hidden},
		{"approve-others-run", func(t string) *httptest.ResponseRecorder {
			return do(h, "POST", "/api/runs/"+run.ID.String()+"/approve", t, map[string]any{"stepId": "x"})
		}, hidden},
		{"users", func(t string) *httptest.ResponseRecorder {
			return do(h, "GET", "/api/users", t, nil)
		}, denied},
	}
	allow := map[string][]string{
		"root":  {"edit", "run", "publish", "members", "see-others-run", "approve-others-run", "users"},
		"ed":    {"edit", "run", "publish", "see-others-run", "approve-others-run"},
		"own":   {"edit", "run", "publish", "members", "see-others-run", "approve-others-run"},
		"med":   {"edit", "run", "see-others-run", "approve-others-run"},
		"plain": {"run"},
	}
	for who, allowed := range allow {
		for _, a := range actions {
			want := false
			for _, x := range allowed {
				want = want || x == a.name
			}
			w := a.call(tok[who])
			if got := !a.gone(w); got != want {
				t.Errorf("%s %s: allowed=%v, want %v (%d %s)", who, a.name, got, want, w.Code, strings.TrimSpace(w.Body.String()))
			}
		}
	}

	// A user lists only the runs it started.
	var runs []*store.Run
	_ = json.Unmarshal(do(h, "GET", "/api/runs", tok["plain"], nil).Body.Bytes(), &runs)
	if len(runs) != 1 || runs[0].ID != mine.ID {
		t.Fatalf("a user's run listing: %+v", runs)
	}
	// An editor sees them all.
	runs = nil
	_ = json.Unmarshal(do(h, "GET", "/api/runs", tok["ed"], nil).Body.Bytes(), &runs)
	if len(runs) != 2 {
		t.Fatalf("an editor's run listing has %d runs", len(runs))
	}
}
