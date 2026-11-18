package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/secrets"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A server whose env store HAS a key.
func keyedServer(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for name, perms := range DefaultRoles() {
		if err := st.UpsertRole(context.Background(), name, perms); err != nil {
			t.Fatal(err)
		}
	}
	key, _ := secrets.NewKey()
	t.Setenv("WFX_TEST_API_CONN_KEY", key)
	eng := engine.New(config.Config{Addr: "0.0.0.0:8090", SecretKeyEnv: "WFX_TEST_API_CONN_KEY"}, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	return New(eng, st, nil, "0.0.0.0:8090", "", nil), st
}

const testCred = "product-issued-credential-0123456789"

func TestAConnectionsCredentialIsSealedAndNeverReturned(t *testing.T) {
	h, st := keyedServer(t)
	f, call := newTenantFixtureOn(t, h, st)
	body := map[string]any{"name": "crm", "endpoint": "https://tools.example.test/mcp", "credential": testCred, "tools": []string{"items.read"}}
	c, b := call("POST", "/api/tenants/acme/connections", f.admin, body)
	if c != 201 || strings.Contains(b, testCred) || !strings.Contains(b, `"hasCredential":true`) {
		t.Fatalf("create: %d %s", c, b)
	}
	if c, _ := call("POST", "/api/tenants/acme/connections", f.admin, body); c != 409 {
		t.Fatalf("duplicate: %d", c)
	}
	c, b = call("GET", "/api/tenants/acme/connections", f.admin, nil)
	if c != 200 || strings.Contains(b, testCred) || !strings.Contains(b, "items.read") {
		t.Fatalf("list: %d %s", c, b)
	}
	var enc []byte
	if err := st.DB().QueryRow(`SELECT credential_enc FROM tenant_connections WHERE name='crm'`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || strings.Contains(string(enc), testCred) {
		t.Fatal("the credential is stored in the clear")
	}
	// Another tenant's connection is absent, for every route.
	if c, _ := call("PUT", "/api/tenants/globex/connections/crm/tools", f.admin, map[string]any{"tools": []string{"x"}}); c != 404 {
		t.Fatalf("tools through another tenant: %d", c)
	}
	if c, _ := call("DELETE", "/api/tenants/globex/connections/crm", f.admin, nil); c != 404 {
		t.Fatalf("delete through another tenant: %d", c)
	}
	if c, b := call("GET", "/api/tenants/globex/connections", f.admin, nil); c != 200 || strings.Contains(b, "crm") {
		t.Fatalf("another tenant lists it: %d %s", c, b)
	}
	// A principal or an account of the tenant cannot manage connections.
	if c, _ := call("GET", "/api/tenants/acme/connections", f.tokA, nil); c != 403 {
		t.Fatalf("a principal listed connections: %d", c)
	}
}

func TestAConnectionCredentialNeedsAKey(t *testing.T) {
	f, call := newTenantFixture(t) // no env-store key
	c, _ := call("POST", "/api/tenants/acme/connections", f.admin,
		map[string]any{"name": "crm", "endpoint": "https://tools.example.test/mcp", "credential": testCred, "tools": []string{"a"}})
	if c != 409 {
		t.Fatalf("a credential was accepted with no key: %d", c)
	}
	if n, _ := f.st.ListConnections(context.Background(), "acme"); len(n) != 0 {
		t.Fatal("a connection was stored anyway")
	}
}

func TestAConnectionEndpointIsHTTPS(t *testing.T) {
	h, st := keyedServer(t)
	f, call := newTenantFixtureOn(t, h, st)
	for _, ep := range []string{"http://tools.example.test/mcp", "https://u:p@tools.example.test/mcp", "ftp://x", "/mcp", ""} {
		if c, _ := call("POST", "/api/tenants/acme/connections", f.admin, map[string]any{"name": "c", "endpoint": ep, "tools": []string{"a"}}); c != 400 {
			t.Errorf("%q: %d", ep, c)
		}
	}
	for _, bad := range []map[string]any{
		{"name": "c", "endpoint": "https://t.example.test", "headerName": "Bad Header"},
		{"name": "c", "endpoint": "https://t.example.test", "tools": []string{"bad tool"}},
		{"name": "bad name", "endpoint": "https://t.example.test"},
	} {
		if c, _ := call("POST", "/api/tenants/acme/connections", f.admin, bad); c != 400 {
			t.Errorf("%v: %d", bad, c)
		}
	}
	if c, b := call("POST", "/api/tenants/acme/connections", f.admin, map[string]any{"name": "local", "endpoint": "http://127.0.0.1:9/mcp", "tools": []string{"a"}}); c != 201 {
		t.Fatalf("loopback http: %d %s", c, b)
	}
}
