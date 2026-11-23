package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/login"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// ORGANIZATIONS AT SIGN-IN (ADR 0035): an OIDC provider with orgClaim places
// each person in one tenant, read from the verified id_token, with a role the
// operator's roleMap decides. The binding is stored (user_tenants) and is the
// only source of a user Subject's Tenant. A provider without orgClaim changes
// nothing: its people stay unscoped (Tenant "").

// orgScopingEnforced says whether an organization's people are actually held
// to their tenant: tenant-scoped visibility and the authoring guard wired
// into the routes (#189). Until then an organization sign-up would let a
// stranger in with an unscoped view and shell steps, so every provider that
// sets orgClaim is REFUSED when the providers are built. The change that
// wires #189 sets this to true, and nothing else does.
const orgScopingEnforced = true

// orgScopingForTest is how a test exercises the sign-in path before #189.
// Nothing outside _test.go files assigns it.
var orgScopingForTest bool

// errOrgScopingMissing is the refusal while orgScopingEnforced is false.
var errOrgScopingMissing = errors.New("orgClaim requires organization scoping (#189), which this build does not include yet")

func orgScopingOn() bool { return orgScopingEnforced || orgScopingForTest }

// The sign-in refusals. Each is logged with its detail; the browser sees only
// "sign-in failed".
var (
	errOrgMissing    = errors.New("the provider named no organization")
	errOrgNotAllowed = errors.New("the organization is not allowed on this provider")
	errOrgUnknown    = errors.New("the organization is not set up on this server")
	errOrgSwitch     = errors.New("this account belongs to another organization")
	errOrgUnbound    = errors.New("unauthenticated: sign in again to join your organization")
)

// orgSignIn is what one sign-in says about the person's organization.
type orgSignIn struct {
	tenant string // store.OrgTenantID(provider, claim)
	name   string // the claim, printable characters only: the tenant's name
	role   string // config.OrgRoleAdmin or config.OrgRoleMember
}

// orgRole looks the role claim's values up in the operator's roleMap. Any
// value mapped to org-admin makes the person an org-admin; everything else,
// a value not in the map or no claim at all, is member. The provider's value
// is only ever a key here, never a role.
func orgRole(c config.AuthProvider, values []string) string {
	for _, v := range values {
		if c.RoleMap[v] == config.OrgRoleAdmin {
			return config.OrgRoleAdmin
		}
	}
	return config.OrgRoleMember
}

// resolveOrg checks a sign-in's organization BEFORE any account is found or
// made, so a refused sign-in leaves no user row behind: the claim must be
// there, allowed by allowedOrgs when that is set, and its tenant must exist
// unless autoCreateOrg says to make it. It returns nil for a provider
// without orgClaim.
func (s *Server) resolveOrg(ctx context.Context, p *login.Provider, id *login.Identity) (*orgSignIn, error) {
	c := p.Config
	if c.OrgClaim == "" {
		return nil, nil
	}
	if id.Org == "" {
		log.Printf("auth: AUDIT sign-in as %q (%s) refused: no %s claim", id.Username, c.Name, c.OrgClaim)
		return nil, errOrgMissing
	}
	if len(c.AllowedOrgs) > 0 && !slices.Contains(c.AllowedOrgs, id.Org) {
		log.Printf("auth: AUDIT sign-in as %q (%s) refused: organization %q is not in allowedOrgs", id.Username, c.Name, id.Org)
		return nil, errOrgNotAllowed
	}
	o := &orgSignIn{tenant: store.OrgTenantID(c.Name, id.Org), name: store.OrgTenantName(id.Org), role: orgRole(c, id.OrgRoles)}
	switch _, err := s.store.GetTenant(ctx, o.tenant); {
	case err == nil:
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	case !c.AutoCreateOrg:
		log.Printf("auth: AUDIT sign-in as %q (%s) refused: organization %q (tenant %s) is not set up and autoCreateOrg is off", id.Username, c.Name, id.Org, o.tenant)
		return nil, errOrgUnknown
	}
	return o, nil
}

// bindOrg puts the signed-in user in the organization resolved above: binds
// a first sign-in, refreshes the role of a repeat one, and refuses one
// naming a different organization than the account is bound to. Creating
// the tenant and binding are one transaction, so a refusal leaves no orphan
// tenant. It runs before role sync, so a refused sign-in changes nothing the
// account holds.
func (s *Server) bindOrg(ctx context.Context, p *login.Provider, u *store.User, o *orgSignIn) error {
	name := p.Config.Name
	cur, curRole, err := s.store.UserTenant(ctx, u.ID)
	if err != nil {
		return err
	}
	if cur != "" && cur != o.tenant {
		log.Printf("auth: AUDIT sign-in as %q (%s) refused: bound to tenant %s, this sign-in names %s", u.Name, name, cur, o.tenant)
		return errOrgSwitch
	}
	created, err := s.store.BindUserTenantCreating(ctx, u.ID, o.tenant, o.name, o.role)
	if errors.Is(err, store.ErrUserTenantBound) {
		// A sign-in naming another organization bound the account between
		// the read above and this write.
		log.Printf("auth: AUDIT sign-in as %q (%s) refused: bound to another tenant than %s", u.Name, name, o.tenant)
		return errOrgSwitch
	} else if err != nil {
		return err
	}
	if created {
		log.Printf("auth: AUDIT tenant %s created on first sign-in by %q (%s), organization %q", o.tenant, u.Name, name, o.name)
	}
	switch {
	case cur == "":
		log.Printf("auth: AUDIT account %q bound to tenant %s as %s (%s)", u.Name, o.tenant, o.role, name)
	case curRole != o.role:
		log.Printf("auth: AUDIT account %q role in tenant %s changed from %s to %s (%s)", u.Name, o.tenant, curRole, o.role, name)
	}
	return nil
}

// orgProviderOf says whether the provider a user is bound to sets orgClaim.
// It reads the store only when some configured provider sets orgClaim, so an
// install without organizations does no extra work per request.
func (s *Server) orgProviderOf(ctx context.Context, u *store.User) (bool, error) {
	any := false
	for _, p := range s.providers {
		any = any || p.Config.OrgClaim != ""
	}
	if !any {
		return false, nil
	}
	prov, err := s.store.UserProvider(ctx, u.ID)
	if err != nil {
		return false, err
	}
	p := s.providers[prov]
	return p != nil && p.Config.OrgClaim != "", nil
}

// orgGuarded says whether the caller is a person in an organization whose
// policy holds them to the customer authoring guard. It fails closed: only a
// provider config that says orgPolicy "full" lifts it, and a lookup that
// fails, or a provider no longer configured, leaves the person guarded.
func (s *Server) orgGuarded(r *http.Request) bool {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.Kind != SubjectUser || sub.Tenant == "" || sub.User == nil {
		return false
	}
	prov, err := s.store.UserProvider(r.Context(), sub.User.ID)
	if err != nil {
		return true
	}
	if p := s.providers[prov]; p != nil && p.Config.OrgPolicy == config.OrgPolicyFull {
		return false
	}
	return true
}

// refuseUnscopedOrgProviders disables every provider that sets orgClaim while
// organization scoping is not enforced (orgScopingEnforced).
func refuseUnscopedOrgProviders(ps map[string]*login.Provider) {
	if orgScopingOn() {
		return
	}
	for name, p := range ps {
		if p.Config.OrgClaim != "" {
			log.Printf("auth: provider disabled: %s: %v", name, errOrgScopingMissing)
			delete(ps, name)
		}
	}
}
