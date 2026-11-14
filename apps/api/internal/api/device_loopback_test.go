package api

import (
	"context"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// The loopback device approval signs in loopback-only accounts (the one it
// creates, again later). It does not stand in for an account that has a
// credential of its own — a password, or an external provider — so a typed
// name on the loopback cannot take that person's session (and their say as an
// approver).
func TestTheLoopbackGrantIsOnlyForLoopbackAccounts(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")
	ctx := context.Background()
	for _, u := range []string{"bob", "local-only", "ann@github"} {
		if err := st.CreateUser(ctx, &store.User{Name: u, DisplayName: u}); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := auth.HashPassword("a-long-test-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(ctx, "bob", hash, false); err != nil {
		t.Fatal(err)
	}
	verify := func(user string) (int, string) {
		start := decode(t, do(h, "POST", "/api/device/code", "", map[string]string{"client_id": "wfx-cli"}))
		res := do(h, "POST", "/api/device/verify", "", map[string]string{"user_code": start["user_code"].(string), "user": user})
		return res.Code, res.Body.String()
	}
	for user, want := range map[string]string{"bob": "has a password", "ann@github": "login provider"} {
		if code, body := verify(user); code == 200 || !strings.Contains(body, want) {
			t.Fatalf("loopback approval for %s: %d %s, want refused (%s)", user, code, body, want)
		}
	}
	for _, user := range []string{"local-only", "first-time"} {
		if code, body := verify(user); code != 200 {
			t.Fatalf("loopback approval for %s: %d %s", user, code, body)
		}
	}
}
