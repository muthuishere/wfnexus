package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// #191: the sign-in limit keys on the client, and only a configured proxy may
// say who the client is.

const proxyPeer = "192.0.2.10"

func testServerProxies(t *testing.T, trusted ...string) http.Handler {
	t.Helper()
	tp, err := config.ParseTrustedProxies(trusted)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	eng := engine.New(config.Config{Addr: remoteAddr, TrustedProxies: tp}, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	return New(eng, st, nil, remoteAddr, "", nil)
}

// signInVia posts a failing sign-in from peer, with the given headers. Each
// attempt names a new user, so only the per-address limit can refuse it.
func signInVia(h http.Handler, peer string, n int, hdr map[string]string) int {
	raw, _ := json.Marshal(map[string]string{"username": "nobody-" + string(rune('a'+n)), "password": "wrong-password-x"})
	req := httptest.NewRequest("POST", "/api/auth/password", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = peer + ":40000"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

// exhaust spends one address's window and reports the status of the attempt
// after it.
func exhaust(h http.Handler, peer string, hdr func(i int) map[string]string) int {
	for i := 0; i < verifyPerMinute; i++ {
		if c := signInVia(h, peer, i, hdr(i)); c == http.StatusTooManyRequests {
			return -1
		}
	}
	return signInVia(h, peer, verifyPerMinute, hdr(verifyPerMinute))
}

func TestTwoClientsBehindATrustedProxyAreLimitedSeparately(t *testing.T) {
	h := testServerProxies(t, "192.0.2.0/24")
	xff := func(client string) func(int) map[string]string {
		// The client's own claim on the left, then what the proxy appended.
		return func(int) map[string]string {
			return map[string]string{"X-Forwarded-For": "203.0.113.250, " + client}
		}
	}
	if c := exhaust(h, proxyPeer, xff("198.51.100.1")); c != http.StatusTooManyRequests {
		t.Fatalf("the first client was not limited: %d", c)
	}
	if c := signInVia(h, proxyPeer, 0, xff("198.51.100.2")(0)); c == http.StatusTooManyRequests {
		t.Fatal("a second client behind the same proxy was locked out by the first")
	}
}

func TestAForgedForwardedForFromAnUntrustedPeerIsIgnored(t *testing.T) {
	h := testServerProxies(t, "192.0.2.0/24")
	forged := func(i int) map[string]string {
		a := netip.AddrFrom4([4]byte{198, 51, 100, byte(i + 1)}).String()
		return map[string]string{"X-Forwarded-For": a, "X-Real-IP": a, "True-Client-IP": a}
	}
	if c := exhaust(h, "203.0.113.5", forged); c != http.StatusTooManyRequests {
		t.Fatalf("a new forwarded address per attempt escaped the limit: %d", c)
	}
}

func TestWithNoTrustedProxiesThePeerIsTheClient(t *testing.T) {
	h := testServerProxies(t)
	forged := func(i int) map[string]string {
		a := netip.AddrFrom4([4]byte{198, 51, 100, byte(i + 1)}).String()
		return map[string]string{"X-Forwarded-For": a, "X-Real-IP": a, "True-Client-IP": a}
	}
	if c := exhaust(h, proxyPeer, forged); c != http.StatusTooManyRequests {
		t.Fatalf("forwarding headers changed the key with no trusted proxy: %d", c)
	}
	if c := signInVia(h, "198.51.100.3", 0, nil); c == http.StatusTooManyRequests {
		t.Fatal("another peer was limited by the first one's attempts")
	}
}

// The parsing itself: rightmost untrusted hop, every header line, a broken
// chain falls back to the peer.
func TestForwardedClient(t *testing.T) {
	trusted, _ := config.ParseTrustedProxies([]string{"192.0.2.0/24", "2001:db8::/32"})
	for _, tc := range []struct {
		name, peer string
		xff        []string
		want       string // "" = the peer stands
	}{
		{"untrusted peer", "203.0.113.5:1", []string{"198.51.100.1"}, ""},
		{"rightmost untrusted", "192.0.2.10:1", []string{"198.51.100.9, 198.51.100.1, 192.0.2.11"}, "198.51.100.1"},
		{"several header lines", "192.0.2.10:1", []string{"198.51.100.9", "198.51.100.2"}, "198.51.100.2"},
		{"client in an earlier header line", "192.0.2.10:1", []string{"203.0.113.250, 198.51.100.6", "192.0.2.11", "192.0.2.12, 192.0.2.13"}, "198.51.100.6"},
		{"no header", "192.0.2.10:1", nil, ""},
		{"all hops trusted", "192.0.2.10:1", []string{"192.0.2.12"}, ""},
		{"garbage hop", "192.0.2.10:1", []string{"198.51.100.1, not-an-ip"}, ""},
		{"v6 proxy, mapped client", "[2001:db8::1]:1", []string{"::ffff:198.51.100.4"}, "198.51.100.4"},
		{"mapped peer", "[::ffff:192.0.2.10]:1", []string{"198.51.100.5"}, "198.51.100.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			got, ok := forwardedClient(r, trusted)
			if tc.want == "" {
				if ok {
					t.Fatalf("got %s, want the peer", got)
				}
				return
			}
			if !ok || got.String() != tc.want {
				t.Fatalf("got %v %v, want %s", got, ok, tc.want)
			}
		})
	}
}

// A sign-in through two trusted hops, each appending its own header line: the
// client named in the first line is the one limited.
func TestAClientInAnEarlierForwardedForLineIsTheKey(t *testing.T) {
	h := testServerProxies(t, "192.0.2.0/24")
	send := func(client string, n int) int {
		raw, _ := json.Marshal(map[string]string{"username": "nobody-" + string(rune('a'+n)), "password": "wrong-password-x"})
		req := httptest.NewRequest("POST", "/api/auth/password", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = proxyPeer + ":40000"
		req.Header.Add("X-Forwarded-For", client)
		req.Header.Add("X-Forwarded-For", "192.0.2.11")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < verifyPerMinute; i++ {
		send("198.51.100.1", i)
	}
	if c := send("198.51.100.1", verifyPerMinute); c != http.StatusTooManyRequests {
		t.Fatalf("the client in the first line was not limited: %d", c)
	}
	if c := send("198.51.100.2", 0); c == http.StatusTooManyRequests {
		t.Fatal("another client was limited: the key was a proxy hop, not the client")
	}
}

func TestProxyWarning(t *testing.T) {
	some, _ := config.ParseTrustedProxies([]string{"192.0.2.0/24"})
	if proxyWarning(false, nil) == "" {
		t.Error("no warning for a non-loopback bind that trusts no proxy")
	}
	if w := proxyWarning(true, nil); w != "" {
		t.Errorf("loopback warned: %s", w)
	}
	if w := proxyWarning(false, some); w != "" {
		t.Errorf("configured proxies warned: %s", w)
	}
}
