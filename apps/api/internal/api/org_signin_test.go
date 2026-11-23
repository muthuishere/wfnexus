package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/login"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Organizations at sign-in (#188, ADR 0035). The sign-in path is exercised
// with orgScopingForTest on; TestOrgClaimIsRefusedUntilScopingIsEnforced
// proves that without it a provider with orgClaim is not enabled at all.

// withOrgScoping switches the sign-in path on for one test.
func withOrgScoping(t *testing.T) {
	t.Helper()
	orgScopingForTest = true
	t.Cleanup(func() { orgScopingForTest = false })
}

// orgConfig is ssoConfig with organization settings.
func orgConfig(op *mockOP, edit func(p *config.AuthProvider)) config.Auth {
	a := ssoConfig(op)
	a.Providers[0].OrgClaim = "tid"
	a.Providers[0].RoleClaim = "roles"
	a.Providers[0].RoleMap = map[string]string{"Org.Admin": config.OrgRoleAdmin, "Org.Reader": config.OrgRoleMember}
	a.Providers[0].AutoCreateOrg = true
	if edit != nil {
		edit(&a.Providers[0])
	}
	return a
}

func orgServer(t *testing.T, op *mockOP, edit func(p *config.AuthProvider)) (http.Handler, *store.Store) {
	t.Helper()
	t.Setenv("TEST_SSO_SECRET", "s3cret-value")
	return testServerPublic(t, orgConfig(op, edit), testPublicURL)
}

// idClaims makes the mock provider's id_token carry these claims.
func idClaims(op *mockOP, extra map[string]any) {
	op.claimsEdit = func(c map[string]any) {
		for k, v := range extra {
			c[k] = v
		}
	}
}

func TestOrgSignIn(t *testing.T) {
	withOrgScoping(t)
	for _, c := range []struct {
		name     string
		edit     func(p *config.AuthProvider)
		precreat string         // a tenant the operator made beforehand
		first    map[string]any // the first sign-in's id_token claims
		second   map[string]any // a second sign-in's, nil for none
		want     int            // the LAST sign-in's status
		tenant   string         // whoami's tenant after it, when 303
		role     string
	}{
		{name: "first sign-in creates the tenant and binds",
			first: map[string]any{"tid": "acme", "roles": []any{"Org.Admin"}},
			want:  303, tenant: "acme", role: config.OrgRoleAdmin},
		{name: "repeat sign-in keeps the binding and refreshes the role",
			first:  map[string]any{"tid": "acme", "roles": []any{"Org.Admin"}},
			second: map[string]any{"tid": "acme", "roles": "Org.Reader"},
			want:   303, tenant: "acme", role: config.OrgRoleMember},
		{name: "a role value not in the map is member",
			first: map[string]any{"tid": "acme", "roles": []any{"Global.Admin", "superadmin"}},
			want:  303, tenant: "acme", role: config.OrgRoleMember},
		{name: "no role claim is member",
			first: map[string]any{"tid": "acme"},
			want:  303, tenant: "acme", role: config.OrgRoleMember},
		{name: "any mapped admin value wins",
			first: map[string]any{"tid": "acme", "roles": []any{"Org.Reader", "Org.Admin"}},
			want:  303, tenant: "acme", role: config.OrgRoleAdmin},
		{name: "a claim that is not a tenant id gets a stable hashed id",
			first: map[string]any{"tid": "Acme Corp"},
			want:  303, tenant: mustOrgTenantID(t, "Acme Corp"), role: config.OrgRoleMember},
		{name: "missing claim is refused",
			first: map[string]any{"roles": "Org.Admin"},
			want:  401},
		{name: "a claim that is not a string is refused",
			first: map[string]any{"tid": 7},
			want:  401},
		{name: "a claim naming the default tenant is refused",
			first: map[string]any{"tid": store.DefaultTenant},
			want:  401},
		{name: "a different organization later is refused",
			first:  map[string]any{"tid": "acme"},
			second: map[string]any{"tid": "globex"},
			want:   401},
		{name: "autoCreateOrg off refuses an unknown organization",
			edit:  func(p *config.AuthProvider) { p.AutoCreateOrg = false },
			first: map[string]any{"tid": "acme"},
			want:  401},
		{name: "autoCreateOrg off admits an organization the operator made",
			edit:     func(p *config.AuthProvider) { p.AutoCreateOrg = false },
			precreat: "acme",
			first:    map[string]any{"tid": "acme"},
			want:     303, tenant: "acme", role: config.OrgRoleMember},
	} {
		t.Run(c.name, func(t *testing.T) {
			op := newMockOP(t, "s3cret-value")
			h, st := orgServer(t, op, c.edit)
			ctx := context.Background()
			if c.precreat != "" {
				if _, err := st.CreateTenant(ctx, c.precreat, c.precreat); err != nil {
					t.Fatal(err)
				}
			}
			idClaims(op, c.first)
			w, _ := oauthFlow(t, h, "sso", nil)
			if c.second != nil {
				if w.Code != 303 {
					t.Fatalf("first sign-in: %d %s", w.Code, w.Body)
				}
				idClaims(op, c.second)
				w, _ = oauthFlow(t, h, "sso", nil)
			}
			if w.Code != c.want {
				t.Fatalf("sign-in: %d %s, want %d", w.Code, w.Body, c.want)
			}
			if c.want != 303 {
				if oauthSession(w) != "" {
					t.Fatal("a refused sign-in set a session")
				}
				if strings.Contains(w.Body.String(), "acme") || strings.Contains(w.Body.String(), "globex") {
					t.Fatalf("the refusal leaks the claim: %s", w.Body)
				}
				u, err := st.UserBySubject(ctx, "sso", "42")
				switch {
				case c.second == nil && !errors.Is(err, store.ErrNotFound):
					t.Fatalf("a refused first sign-in made an account: %v %v", u, err)
				case c.second != nil:
					// The org switch: the account keeps its first binding.
					if tn, _, _ := st.UserTenant(ctx, u.ID); tn != "acme" {
						t.Fatalf("binding after a refused switch = %q", tn)
					}
					if _, err := st.GetTenant(ctx, "globex"); !errors.Is(err, store.ErrNotFound) {
						t.Fatalf("a refused switch created its tenant: %v", err)
					}
				}
				return
			}
			who := whoami(t, h, w)
			if who["tenant"] != c.tenant || who["orgRole"] != c.role {
				t.Fatalf("whoami tenant=%v orgRole=%v, want %s %s", who["tenant"], who["orgRole"], c.tenant, c.role)
			}
			if _, err := st.GetTenant(ctx, c.tenant); err != nil {
				t.Fatalf("tenant %s: %v", c.tenant, err)
			}
		})
	}
}

func mustOrgTenantID(t *testing.T, claim string) string {
	t.Helper()
	id, err := orgTenantID(claim)
	if err != nil || !store.ValidTenantID(id) || id == claim {
		t.Fatalf("orgTenantID(%q) = %q, %v", claim, id, err)
	}
	if again, _ := orgTenantID(claim); again != id {
		t.Fatalf("orgTenantID is not stable: %q then %q", id, again)
	}
	return id
}

// The org claim is read from the signed id_token, never from userinfo: a
// claim only userinfo carries is as good as no claim.
func TestOrgClaimIsReadFromTheIDTokenOnly(t *testing.T) {
	withOrgScoping(t)
	op := newMockOP(t, "s3cret-value")
	op.userinfo["tid"] = "acme"
	h, _ := orgServer(t, op, nil)
	if w, _ := oauthFlow(t, h, "sso", nil); w.Code != 401 {
		t.Fatalf("sign-in with tid only in userinfo: %d %s", w.Code, w.Body)
	}
}

// Without orgClaim on any provider nothing changes: the person signs in as
// before, with their mapped roles, and is unscoped.
func TestNoOrgClaimLeavesSignInUnchanged(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	idClaims(op, map[string]any{"tid": "acme", "roles": "Org.Admin"})
	h, st := oidcServerStore(t, op)
	w, _ := oauthFlow(t, h, "sso", nil)
	if w.Code != 303 {
		t.Fatalf("sign-in: %d %s", w.Code, w.Body)
	}
	who := whoami(t, h, w)
	if who["name"] != "bob@sso" || who["role"] != RoleSuperadmin || who["tenant"] != "" || who["orgRole"] != "" {
		t.Fatalf("whoami: %v", who)
	}
	if _, err := st.GetTenant(context.Background(), "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a tenant was created without orgClaim: %v", err)
	}
}

// While organization scoping (#189) is not enforced, a provider that sets
// orgClaim is not enabled: it is not offered and its sign-in is not served.
func TestOrgClaimIsRefusedUntilScopingIsEnforced(t *testing.T) {
	if orgScopingEnforced {
		t.Skip("organization scoping is enforced in this build")
	}
	op := newMockOP(t, "s3cret-value")
	h, _ := orgServer(t, op, nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if strings.Contains(res.Body.String(), `"sso"`) {
		t.Fatalf("providers lists the refused provider: %s", res.Body)
	}
	start := httptest.NewRecorder()
	h.ServeHTTP(start, httptest.NewRequest("GET", "/api/auth/oauth/sso/start", nil))
	if start.Code == 302 {
		t.Fatalf("start of a refused provider: %d %s", start.Code, start.Header())
	}
}

// orgGuarded holds an organization's person to the guard unless their
// provider says orgPolicy "full". It fails closed: a provider no longer
// configured leaves them guarded. An unscoped person is never guarded here.
func TestOrgGuarded(t *testing.T) {
	for _, c := range []struct {
		name     string
		policy   string // "" = the default
		provider bool   // the user's provider is still configured
		tenant   string
		want     bool
	}{
		{"default policy is guarded", "", true, "acme", true},
		{"guarded", config.OrgPolicyGuarded, true, "acme", true},
		{"full", config.OrgPolicyFull, true, "acme", false},
		{"provider removed is guarded", config.OrgPolicyFull, false, "acme", true},
		{"unscoped person", "", true, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, st := testServerPublic(t, config.Auth{}, testPublicURL)
			ctx := context.Background()
			u := &store.User{Name: "bob@sso"}
			if err := st.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			if ok, err := st.SetSubject(ctx, u.ID, "sso", "42"); err != nil || !ok {
				t.Fatalf("SetSubject: %v %v", ok, err)
			}
			pc := config.AuthProvider{Name: "sso", Type: "oidc", OrgClaim: "tid", OrgPolicy: c.policy}
			if err := pc.NormalizeOrg(); err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st, providers: map[string]*login.Provider{}}
			if c.provider {
				s.providers["sso"] = &login.Provider{Config: pc}
			}
			r := httptest.NewRequest("GET", "/", nil)
			r = r.WithContext(withSubject(ctx, &Subject{Kind: SubjectUser, Name: u.Name, User: u, Tenant: c.tenant}))
			if got := s.orgGuarded(r); got != c.want {
				t.Fatalf("orgGuarded = %v, want %v", got, c.want)
			}
		})
	}
}

// Config validation: a half-written organization block, or one on a
// provider with no id_token, is refused when the providers are built.
func TestOrgConfigValidation(t *testing.T) {
	for _, c := range []struct {
		name string
		p    config.AuthProvider
		ok   bool
	}{
		{"no org settings", config.AuthProvider{Type: "oidc"}, true},
		{"orgClaim alone", config.AuthProvider{Type: "oidc", OrgClaim: "tid"}, true},
		{"google", config.AuthProvider{Type: "google", OrgClaim: "hd"}, true},
		{"github has no id_token", config.AuthProvider{Type: "github", OrgClaim: "org"}, false},
		{"ldap", config.AuthProvider{Type: "ldap", OrgClaim: "o"}, false},
		{"roleClaim without orgClaim", config.AuthProvider{Type: "oidc", RoleClaim: "roles"}, false},
		{"roleMap without orgClaim", config.AuthProvider{Type: "oidc", RoleMap: map[string]string{"a": "member"}}, false},
		{"autoCreateOrg without orgClaim", config.AuthProvider{Type: "oidc", AutoCreateOrg: true}, false},
		{"orgPolicy without orgClaim", config.AuthProvider{Type: "oidc", OrgPolicy: "full"}, false},
		{"roleMap without roleClaim", config.AuthProvider{Type: "oidc", OrgClaim: "tid", RoleMap: map[string]string{"a": "member"}}, false},
		{"roleMap to a server role", config.AuthProvider{Type: "oidc", OrgClaim: "tid", RoleClaim: "roles", RoleMap: map[string]string{"a": "superadmin"}}, false},
		{"unknown orgPolicy", config.AuthProvider{Type: "oidc", OrgClaim: "tid", OrgPolicy: "open"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := c.p
			err := p.NormalizeOrg()
			if (err == nil) != c.ok {
				t.Fatalf("NormalizeOrg: %v, want ok=%v", err, c.ok)
			}
			if c.ok && p.OrgClaim != "" && p.OrgPolicy != config.OrgPolicyGuarded {
				t.Fatalf("orgPolicy default = %q", p.OrgPolicy)
			}
		})
	}
}
