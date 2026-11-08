package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
)

// ADR 0028: the CLI refreshes an expired access JWT before a request and saves
// the new one; a live one is used as is.
func TestTheCLIRefreshesAnExpiredJWT(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	expired := auth.SignJWT(key, auth.Claims{Sub: "u", Sid: "s"}, time.Now().Add(-time.Hour), auth.AccessTTL)
	fresh := auth.SignJWT(key, auth.Claims{Sub: "u", Sid: "s"}, time.Now(), auth.AccessTTL)

	refreshes := 0
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/refresh" {
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["refresh_token"] != "wfx_refresh" {
				w.WriteHeader(401)
				return
			}
			refreshes++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fresh, "token_type": "Bearer"})
			return
		}
		seen <- r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	tempContexts(t)
	if err := putSession(srv.URL, expired, "wfx_refresh", "ana"); err != nil {
		t.Fatal(err)
	}
	if err := call("GET", "/api/health", nil, nil); err != nil {
		t.Fatal(err)
	}
	if h := <-seen; h != "Bearer "+fresh || refreshes != 1 {
		t.Fatalf("sent %q after %d refreshes", h, refreshes)
	}
	// Saved: the next call uses the new token without refreshing again.
	if err := call("GET", "/api/health", nil, nil); err != nil {
		t.Fatal(err)
	}
	if h := <-seen; h != "Bearer "+fresh || refreshes != 1 {
		t.Fatalf("second call sent %q after %d refreshes", h, refreshes)
	}
}
