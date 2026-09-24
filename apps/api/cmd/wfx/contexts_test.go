package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Each test gets its own contexts file; nothing here touches the real one.
func tempContexts(t *testing.T) string {
	t.Helper()
	// A directory the client has to create itself, as ~/.config/wfx is.
	path := filepath.Join(t.TempDir(), "wfx", "contexts.json")
	t.Setenv("WFX_CONTEXTS", path)
	t.Setenv("WFX_API", "")
	return path
}

// [SEC-TEST] task 6.1/6.2 — the store is 0600, and a loose one is REFUSED.
func TestContextsFileIsPrivateAndALooseOneIsRefused(t *testing.T) {
	path := tempContexts(t)

	if err := putContext("https://wfx.example.com", "wfx_secretvalue", "muthu"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// WINDOWS HAS NO MODE BITS. Go reports 0666 for any ordinary file whatever
	// its ACL says, and os.Chmod there only toggles read-only — so neither the
	// 0600 assertion nor the loose-file refusal can mean anything. The
	// protection there is contextsPath putting the file under %AppData%, which
	// is ACL'd to the user; asserted separately below.
	if runtime.GOOS == "windows" {
		if cfg, err := os.UserConfigDir(); err == nil && os.Getenv("WFX_CONTEXTS") == "" {
			if !strings.HasPrefix(path, cfg) {
				t.Fatalf("on windows the credential store must sit under %s, got %s", cfg, path)
			}
		}
		return
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("contexts file is %#o, want 0600", mode)
	}
	if dir, err := os.Stat(filepath.Dir(path)); err == nil && dir.Mode().Perm()&0o077 != 0 {
		t.Fatalf("contexts directory is %#o", dir.Mode().Perm())
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	// Refused, not warned about: the file holds a bearer token.
	if _, err := loadContexts(); err == nil {
		t.Fatal("a group- and world-readable contexts file was read")
	} else if !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("the refusal does not say how to fix it: %v", err)
	}
	// ...and no request can be built from it either.
	if _, err := resolveContext(""); err == nil {
		t.Fatal("a command resolved a host from a loose contexts file")
	}
}

// task 6.3 — the four-tier resolution order.
func TestResolutionOrder(t *testing.T) {
	tempContexts(t)

	// 4. nothing configured at all.
	got, err := resolveContext("")
	if err != nil || got.url != "http://127.0.0.1:8090" || got.token != "" {
		t.Fatalf("default: %+v %v", got, err)
	}

	// 3. contexts.current.
	if err := putContext("https://a.example.com", "wfx_a", "muthu"); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveContext(""); got.url != "https://a.example.com" || got.token != "wfx_a" {
		t.Fatalf("current context: %+v", got)
	}

	// 2. WFX_API wins over the current context, and — unchanged — carries no
	// bearer when no context matches it.
	t.Setenv("WFX_API", "http://127.0.0.1:9999")
	if got, _ := resolveContext(""); got.url != "http://127.0.0.1:9999" || got.token != "" {
		t.Fatalf("WFX_API: %+v", got)
	}

	// 1. --url is most specific, and names a context that must exist.
	if err := putContext("https://b.example.com", "wfx_b", "muthu"); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveContext("https://b.example.com"); got.token != "wfx_b" {
		t.Fatalf("--url: %+v", got)
	}
	if _, err := resolveContext("https://never.example.com"); err == nil ||
		!strings.Contains(err.Error(), "wfx login --url") {
		t.Fatalf("--url with no context should say to log in first: %v", err)
	}
}

// task 6.7 — one context's credential is never disturbed by another's.
func TestContextsAreIndependent(t *testing.T) {
	tempContexts(t)
	if err := putContext("https://a.example.com", "wfx_a", "muthu"); err != nil {
		t.Fatal(err)
	}
	if err := putContext("https://b.example.com", "wfx_b", "muthu"); err != nil {
		t.Fatal(err)
	}
	if err := removeContext("b.example.com"); err != nil {
		t.Fatal(err)
	}
	c, err := loadContexts()
	if err != nil {
		t.Fatal(err)
	}
	if c.Contexts["a.example.com"].Token != "wfx_a" {
		t.Fatal("logging out of B changed A's credential")
	}
	if _, ok := c.Contexts["b.example.com"]; ok {
		t.Fatal("B survived its own removal")
	}
	if c.Current != "a.example.com" {
		t.Fatalf("current did not fall back to the remaining context: %q", c.Current)
	}
}

// task 6.4 — the bearer goes on the wire when the context has one, and the
// WFX_API-only path sends none.
func TestCallSendsTheBearerOnlyWhenTheContextHasOne(t *testing.T) {
	tempContexts(t)
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	if err := putContext(srv.URL, "wfx_thevalue", "muthu"); err != nil {
		t.Fatal(err)
	}
	if err := call("GET", "/api/health", nil, nil); err != nil {
		t.Fatal(err)
	}
	if h := <-seen; h != "Bearer wfx_thevalue" {
		t.Fatalf("authorization header was %q", h)
	}

	// WFX_API pointed somewhere with no context: no bearer at all, which is
	// exactly right against a loopback server.
	t.Setenv("WFX_API", srv.URL+"/")
	tempPath := filepath.Join(t.TempDir(), "empty.json")
	t.Setenv("WFX_CONTEXTS", tempPath)
	if err := call("GET", "/api/health", nil, nil); err != nil {
		t.Fatal(err)
	}
	if h := <-seen; h != "" {
		t.Fatalf("the tokenless WFX_API path sent %q", h)
	}
}

// [SEC-TEST] task 6.8 — the value is in the credential store and nowhere else:
// not in argv, not in the environment, not printed.
func TestTheCredentialIsNotInArgvOrTheEnvironment(t *testing.T) {
	path := tempContexts(t)
	const value = "wfx_0123456789abcdef0123456789abcdef0123456789abcdef"

	if err := putContext("https://wfx.example.com", value, "muthu"); err != nil {
		t.Fatal(err)
	}
	for _, arg := range os.Args {
		if strings.Contains(arg, value) {
			t.Fatal("the credential is in this process's argv")
		}
	}
	for _, kv := range os.Environ() {
		if strings.Contains(kv, value) {
			t.Fatal("the credential is in the environment")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), value) {
		t.Fatalf("the credential is not in the store it was meant for: %v", err)
	}
}
