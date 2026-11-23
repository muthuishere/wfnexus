package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"

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
const orgScopingEnforced = false

// orgScopingForTest is how a test exercises the sign-in path before #189.
// Nothing outside _test.go files assigns it.
var orgScopingForTest bool

// errOrgScopingMissing is the refusal while orgScopingEnforced is false.
var errOrgScopingMissing = errors.New("orgClaim requires organization scoping (#189), which this build does not include yet")

func orgScopingOn() bool { return orgScopingEnforced || orgScopingForTest }

// The sign-in refusals. Each is logged with its detail; the browser sees only
// "sign-in failed".
var (
	errOrgMissing  = errors.New("the provider named no organization")
	errOrgUnknown  = errors.New("the organization is not set up on this server")
	errOrgReserved = errors.New("the organization claim names the default tenant")
	errOrgSwitch   = errors.New("this account belongs to another organization")
)

// orgSignIn is what one sign-in says about the person's organization.
type orgSignIn struct {
	tenant string // the tenant id derived from the claim
	name   string // the claim's raw value, kept as the tenant's name
	role   string // config.OrgRoleAdmin or config.OrgRoleMember
	exists bool   // the tenant is already there
}

// orgTenantID turns an organization claim into a tenant id. A value that is
// already a valid tenant id is used as it is, unchanged: lowercasing would
// merge "Acme" into "acme". Any other value becomes "org-" plus 32 hex
// characters of its sha256: stable for the same claim, valid, and long
// enough that two organizations do not meet by accident.
//
// The default tenant owns every project not bound elsewhere, so a claim
// naming it is refused rather than hashed: an operator who sees that error
// in the log has a provider worth looking at.
func orgTenantID(claim string) (string, error) {
	if claim == store.DefaultTenant {
		return "", errOrgReserved
	}
	if store.ValidTenantID(claim) {
		return claim, nil
	}
	sum := sha256.Sum256([]byte(claim))
	return "org-" + hex.EncodeToString(sum[:16]), nil
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
// there, and the tenant must exist unless autoCreateOrg says to make it. It
// returns nil for a provider without orgClaim.
func (s *Server) resolveOrg(ctx context.Context, p *login.Provider, id *login.Identity) (*orgSignIn, error) {
	c := p.Config
	if c.OrgClaim == "" {
		return nil, nil
	}
	if id.Org == "" {
		log.Printf("auth: AUDIT sign-in as %s (%s) refused: no %s claim", id.Username, c.Name, c.OrgClaim)
		return nil, errOrgMissing
	}
	tenant, err := orgTenantID(id.Org)
	if err != nil {
		log.Printf("auth: AUDIT sign-in as %s (%s) refused: %s claim %q: %v", id.Username, c.Name, c.OrgClaim, id.Org, err)
		return nil, err
	}
	o := &orgSignIn{tenant: tenant, name: id.Org, role: orgRole(c, id.OrgRoles)}
	switch _, err := s.store.GetTenant(ctx, tenant); {
	case err == nil:
		o.exists = true
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	case !c.AutoCreateOrg:
		log.Printf("auth: AUDIT sign-in as %s (%s) refused: organization %q (tenant %s) is not set up and autoCreateOrg is off", id.Username, c.Name, id.Org, tenant)
		return nil, errOrgUnknown
	}
	return o, nil
}

// bindOrg puts the signed-in user in the organization resolved above: makes
// the tenant if it is new, binds a first sign-in, refreshes the role of a
// repeat one, and refuses one naming a different organization than the
// account is bound to. It runs before role sync, so a refused sign-in
// changes nothing the account holds.
func (s *Server) bindOrg(ctx context.Context, p *login.Provider, u *store.User, o *orgSignIn) error {
	name := p.Config.Name
	cur, curRole, err := s.store.UserTenant(ctx, u.ID)
	if err != nil {
		return err
	}
	if cur != "" && cur != o.tenant {
		log.Printf("auth: AUDIT sign-in as %s (%s) refused: bound to tenant %s, this sign-in names %s", u.Name, name, cur, o.tenant)
		return errOrgSwitch
	}
	if !o.exists {
		if _, err := s.store.CreateTenant(ctx, o.tenant, o.name); err == nil {
			log.Printf("auth: AUDIT tenant %s created on first sign-in by %s (%s), organization %q", o.tenant, u.Name, name, o.name)
		} else if !errors.Is(err, store.ErrTenantExists) {
			// ErrTenantExists: a concurrent first sign-in made it.
			return err
		}
	}
	if err := s.store.BindUserTenant(ctx, u.ID, o.tenant, o.role); errors.Is(err, store.ErrUserTenantBound) {
		log.Printf("auth: AUDIT sign-in as %s (%s) refused: bound to another tenant than %s", u.Name, name, o.tenant)
		return errOrgSwitch
	} else if err != nil {
		return err
	}
	switch {
	case cur == "":
		log.Printf("auth: AUDIT account %s bound to tenant %s as %s (%s)", u.Name, o.tenant, o.role, name)
	case curRole != o.role:
		log.Printf("auth: AUDIT account %s role in tenant %s changed from %s to %s (%s)", u.Name, o.tenant, curRole, o.role, name)
	}
	return nil
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
