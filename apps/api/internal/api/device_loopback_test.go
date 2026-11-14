package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// The loopback device approval creates the install's FIRST account, as its
// superadmin owner, and signs loopback-only accounts in again later. Any
// other new name is refused, so nobody at the machine can mint a new account
// (a fresh "mallory", superadmin or not) to get past an approver policy or
// prevent_self_approval. An account with a credential of its own (a password,
// an external provider) signs in with it.
func TestTheLoopbackGrantIsOnlyForTheFirstAndLoopbackAccounts(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")
	ctx := context.Background()
	// Each approval from its own source address, so the per-source limit on
	// refused approvals (RFC 8628 §5.2) does not answer for the rule.
	src := 0
	verify := func(user string) (int, string) {
		start := decode(t, do(h, "POST", "/api/device/code", "", map[string]string{"client_id": "wfx-cli"}))
		raw, _ := json.Marshal(map[string]string{"user_code": start["user_code"].(string), "user": user})
		req := httptest.NewRequest("POST", "/api/device/verify", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		src++
		req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", src)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res.Code, res.Body.String()
	}

	// An empty install: the first name is its owner.
	if code, body := verify("owner"); code != 200 {
		t.Fatalf("first account: %d %s", code, body)
	}
	if u, err := st.UserByName(ctx, "owner"); err != nil || u.Role != "superadmin" {
		t.Fatalf("the first account = %+v (err %v), want superadmin", u, err)
	}
	// The owner signs in again later.
	if code, body := verify("owner"); code != 200 {
		t.Fatalf("owner again: %d %s", code, body)
	}
	// Any later new name is refused and nothing is created.
	for _, name := range []string{"mallory", "mallory2"} {
		if code, body := verify(name); code == 200 || !strings.Contains(body, "creates only the first account") {
			t.Fatalf("new name %s on loopback: %d %s, want refused", name, code, body)
		}
		if _, err := st.UserByName(ctx, name); err == nil {
			t.Fatalf("%s was created", name)
		}
	}
	if n, _ := st.CountUsersWithRole(ctx, "superadmin"); n != 1 {
		t.Fatalf("%d superadmins, want only the owner", n)
	}

	// Accounts an admin made: one with a password, one from a provider, one
	// with neither (a loopback-only account).
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
	for user, want := range map[string]string{"bob": "has a password", "ann@github": "login provider"} {
		if code, body := verify(user); code == 200 || !strings.Contains(body, want) {
			t.Fatalf("loopback approval for %s: %d %s, want refused (%s)", user, code, body, want)
		}
	}
	if code, body := verify("local-only"); code != 200 {
		t.Fatalf("loopback-only account: %d %s", code, body)
	}
}
