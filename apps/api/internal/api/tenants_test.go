package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// tenantFixture: an auth-required server, a superadmin, two tenants each with
// one bound project, one run in each, and one service principal per tenant.
type tenantFixture struct {
	h            http.Handler
	st           *store.Store
	admin        string
	tokA, tokB   string
	runA, runB   string
	principalAID string
}

func newTenantFixture(t *testing.T) (*tenantFixture, func(method, path, tok string, body any) (int, string)) {
	t.Helper()
	h, st := testServer(t, "0.0.0.0:8090")
	ctx := context.Background()
	u := &store.User{Name: "root", Role: RoleSuperadmin}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	f := &tenantFixture{h: h, st: st, admin: jwtFor(t, st, u, "")}
	call := func(method, path, tok string, body any) (int, string) {
		res := do(h, method, path, tok, body)
		return res.Code, res.Body.String()
	}
	mk := func(tenant, project string) (tok, run, pid string) {
		if c, b := call("POST", "/api/tenants", f.admin, map[string]string{"id": tenant}); c != 201 {
			t.Fatalf("create tenant: %d %s", c, b)
		}
		if c, b := call("POST", "/api/tenants/"+tenant+"/projects", f.admin, map[string]string{"project": project}); c != 200 {
			t.Fatalf("bind: %d %s", c, b)
		}
		c, b := call("POST", "/api/tenants/"+tenant+"/principals", f.admin, map[string]string{"name": "svc"})
		if c != 201 {
			t.Fatalf("principal: %d %s", c, b)
		}
		var got struct {
			Token     string `json:"token"`
			Principal struct {
				ID string `json:"id"`
			} `json:"principal"`
		}
		_ = json.Unmarshal([]byte(b), &got)
		r, err := st.CreateRun(ctx, project, "wf", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		return got.Token, r.ID.String(), got.Principal.ID
	}
	f.tokA, f.runA, f.principalAID = mk("acme", "proj-a")
	f.tokB, f.runB, _ = mk("globex", "proj-b")
	return f, call
}

func TestAServicePrincipalActsInTheTenantOfItsCredential(t *testing.T) {
	f, call := newTenantFixture(t)
	c, b := call("GET", "/api/svc/self", f.tokA, nil)
	if c != 200 || !strings.Contains(b, `"tenant":"acme"`) || !strings.Contains(b, `"proj-a"`) {
		t.Fatalf("self: %d %s", c, b)
	}
	if strings.Contains(b, f.tokA) {
		t.Fatal("self echoes the token")
	}
	// Its own run: yes. The other tenant's: the SAME answer as a run that
	// does not exist.
	if c, _ := call("GET", "/api/svc/runs/"+f.runA, f.tokA, nil); c != 200 {
		t.Fatalf("own run: %d", c)
	}
	cx, bx := call("GET", "/api/svc/runs/"+f.runB, f.tokA, nil)
	ca, ba := call("GET", "/api/svc/runs/00000000-0000-4000-8000-000000000000", f.tokA, nil)
	if cx != 404 || cx != ca || bx != ba {
		t.Fatalf("cross-tenant %d %q vs absent %d %q", cx, bx, ca, ba)
	}
	c, b = call("GET", "/api/svc/runs", f.tokA, nil)
	if c != 200 || !strings.Contains(b, f.runA) || strings.Contains(b, f.runB) {
		t.Fatalf("list: %d %s", c, b)
	}
}

// Nothing the caller sends can name another tenant: the query, a header and
// a body are all ignored, because the tenant is read off the stored principal.
func TestTheTenantComesFromTheCredentialNotTheRequest(t *testing.T) {
	f, _ := newTenantFixture(t)
	srv := f.h
	for _, path := range []string{
		"/api/svc/runs?tenant=globex",
		"/api/svc/runs/" + f.runB + "?tenant=globex",
	} {
		res := doWithHeaders(srv, "GET", path, f.tokA, map[string]string{"tenant": "globex"},
			map[string]string{"X-Tenant": "globex", "X-Tenant-Id": "globex"})
		if strings.Contains(res.Body.String(), f.runB) {
			t.Fatalf("%s: the request named another tenant and was served it: %d %s", path, res.Code, res.Body)
		}
	}
}

// A service principal reaches /api/svc/ and NOTHING else, including routes a
// signed-in person may read and the public ones that describe a subject.
func TestAServicePrincipalIsRefusedEverywhereElse(t *testing.T) {
	f, call := newTenantFixture(t)
	for _, rt := range [][2]string{
		{"GET", "/api/runs"},
		{"GET", "/api/runs/" + f.runA},
		{"GET", "/api/workflows"},
		{"GET", "/api/projects"},
		{"GET", "/api/skills"},
		{"GET", "/api/state"},
		{"GET", "/api/whoami"},
		{"GET", "/api/tenants"},
		{"POST", "/api/tenants"},
		{"GET", "/api/tenants/acme/principals"},
		{"GET", "/api/users"},
		{"POST", "/api/workflows/wf/runs"},
		{"POST", "/api/tokens/revoke"},
		{"GET", "/api/svc%2F..%2Fruns"},
	} {
		c, b := call(rt[0], rt[1], f.tokA, map[string]string{})
		if c != 403 && c != 404 && c != 405 {
			t.Fatalf("%s %s served a service principal: %d %s", rt[0], rt[1], c, b)
		}
		if strings.Contains(b, f.runA) || strings.Contains(b, f.runB) {
			t.Fatalf("%s %s returned run data to a service principal", rt[0], rt[1])
		}
	}
}

// The tenant routes are for a service principal only: a person (even a
// superadmin) and an anonymous loopback caller have no tenant to act in.
func TestTheTenantRoutesRefuseEveryoneElse(t *testing.T) {
	f, call := newTenantFixture(t)
	if c, _ := call("GET", "/api/svc/runs", f.admin, nil); c != 401 {
		t.Fatalf("a superadmin reached the tenant routes: %d", c)
	}
	if c, _ := call("GET", "/api/svc/runs", "", nil); c != 401 {
		t.Fatalf("no credential reached the tenant routes: %d", c)
	}
	lh, lst := testServer(t, "127.0.0.1:8090")
	if _, err := lst.CreateRun(context.Background(), "p", "wf", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if res := do(lh, "GET", "/api/svc/runs", "", nil); res.Code != 401 {
		t.Fatalf("an anonymous loopback caller reached the tenant routes: %d %s", res.Code, res.Body)
	}
}

func TestARevokedPrincipalStopsAtOnce(t *testing.T) {
	f, call := newTenantFixture(t)
	if c, b := call("DELETE", "/api/tenants/acme/principals/"+f.principalAID, f.admin, nil); c != 200 {
		t.Fatalf("revoke: %d %s", c, b)
	}
	if c, _ := call("GET", "/api/svc/self", f.tokA, nil); c != 401 {
		t.Fatalf("a revoked token still works: %d", c)
	}
	// Revoking through the wrong tenant is an absent principal.
	if c, _ := call("DELETE", "/api/tenants/globex/principals/"+f.principalAID, f.admin, nil); c != 404 {
		t.Fatalf("revoke through another tenant: %d", c)
	}
	if c, _ := call("GET", "/api/svc/self", f.tokB, nil); c != 200 {
		t.Fatalf("the other tenant's principal stopped too: %d", c)
	}
}

func TestTenantAdministrationIsAnOperatorsAndShowsATokenOnce(t *testing.T) {
	f, call := newTenantFixture(t)
	ctx := context.Background()
	u := &store.User{Name: "plain"}
	if err := f.st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	plain := jwtFor(t, f.st, u, "")
	for _, rt := range [][2]string{
		{"GET", "/api/tenants"},
		{"POST", "/api/tenants"},
		{"POST", "/api/tenants/acme/principals"},
		{"POST", "/api/tenants/acme/projects"},
	} {
		if c, _ := call(rt[0], rt[1], plain, map[string]string{"id": "x", "name": "x", "project": "x"}); c != 403 {
			t.Fatalf("%s %s by a non-operator: %d", rt[0], rt[1], c)
		}
	}
	c, b := call("GET", "/api/tenants/acme/principals", f.admin, nil)
	if c != 200 || strings.Contains(b, f.tokA) || strings.Contains(b, "token_hash") || strings.Contains(b, `"token"`) {
		t.Fatalf("listing principals: %d %s", c, b)
	}
	// A project already in one tenant cannot be put in another.
	if c, _ := call("POST", "/api/tenants/globex/projects", f.admin, map[string]string{"project": "proj-a"}); c != 409 {
		t.Fatalf("moving a bound project: %d", c)
	}
	// The declared surface is validated and shown; a URL carrying
	// credentials is refused.
	if c, _ := call("PUT", "/api/tenants/acme/principals/"+f.principalAID+"/surface", f.admin,
		map[string]any{"endpoint": "https://user:pw@tools.example.test/mcp", "tools": []string{"a"}}); c != 400 {
		t.Fatalf("endpoint with credentials: %d", c)
	}
	c, b = call("PUT", "/api/tenants/acme/principals/"+f.principalAID+"/surface", f.admin,
		map[string]any{"endpoint": "https://tools.example.test/mcp", "tools": []string{"leads.read", "leads.create"}})
	if c != 200 || !strings.Contains(b, "leads.create") {
		t.Fatalf("surface: %d %s", c, b)
	}
	if c, _ := call("PUT", "/api/tenants/globex/principals/"+f.principalAID+"/surface", f.admin,
		map[string]any{"endpoint": "https://x.example.test", "tools": []string{}}); c != 404 {
		t.Fatalf("surface through another tenant: %d", c)
	}
	if _, b := call("GET", "/api/svc/self", f.tokA, nil); !strings.Contains(b, "leads.read") {
		t.Fatalf("self does not show the declared surface: %s", b)
	}
}
