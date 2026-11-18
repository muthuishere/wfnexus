package login

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// A discovery that fails (the provider down, a timeout) is not remembered
// for good: after a short pause the next sign-in tries again and succeeds.
func TestATransientDiscoveryFailureRecovers(t *testing.T) {
	old := discoverBackoff
	discoverBackoff = 20 * time.Millisecond
	defer func() { discoverBackoff = old }()

	var calls, failures atomic.Int32
	failures.Store(2)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			w.WriteHeader(404)
			return
		}
		calls.Add(1)
		if failures.Add(-1) >= 0 {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint": srv.URL + "/token", "userinfo_endpoint": srv.URL + "/userinfo", "jwks_uri": srv.URL + "/jwks",
		})
	}))
	defer srv.Close()
	o, err := NewOAuth(config.AuthProvider{Name: "sso", Type: "oidc", Issuer: srv.URL, ClientID: "wfx"})
	if err != nil {
		t.Fatal(err)
	}
	a := NewAuthRequest()
	if _, err := o.AuthCodeURL(context.Background(), a, "https://w/cb"); err == nil {
		t.Fatal("the first discovery should fail")
	}
	// Within the pause: no new attempt, the same error.
	if _, err := o.AuthCodeURL(context.Background(), a, "https://w/cb"); err == nil || calls.Load() != 1 {
		t.Fatalf("retried at once: calls=%d err=%v", calls.Load(), err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		time.Sleep(30 * time.Millisecond)
		if u, err := o.AuthCodeURL(context.Background(), a, "https://w/cb"); err == nil {
			if u == "" {
				t.Fatal("empty URL")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery never recovered after %d calls", calls.Load())
		}
	}
	// Once it succeeded it is kept: no more discovery calls.
	n := calls.Load()
	_, _ = o.AuthCodeURL(context.Background(), a, "https://w/cb")
	if calls.Load() != n {
		t.Fatal("a successful discovery was repeated")
	}
	// A cancelled request context does not make discovery fail.
	o2, _ := NewOAuth(config.AuthProvider{Name: "sso", Type: "oidc", Issuer: srv.URL, ClientID: "wfx"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o2.AuthCodeURL(ctx, a, "https://w/cb"); err != nil {
		t.Fatalf("a cancelled request broke discovery: %v", err)
	}
}
