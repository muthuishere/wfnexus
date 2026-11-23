package config

import (
	"errors"
	"fmt"
	"strings"
)

// Auth is the `auth:` block of the server config (ADR 0028). Providers only
// AUTHENTICATE; roles stay wfnexus's own (members table, users.role).
type Auth struct {
	Providers []AuthProvider `yaml:"providers,omitempty"`
}

// AuthProvider is one login provider. Secrets are never values here: each is
// the NAME of an environment variable.
//
//	type: password | oidc | google | github | ldap | mock-ldap
type AuthProvider struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`

	// oidc / google / github
	Issuer          string   `yaml:"issuer,omitempty"`
	ClientID        string   `yaml:"clientId,omitempty"`
	ClientSecretEnv string   `yaml:"clientSecretEnv,omitempty"`
	AuthURL         string   `yaml:"authUrl,omitempty"`
	TokenURL        string   `yaml:"tokenUrl,omitempty"`
	UserInfoURL     string   `yaml:"userInfoUrl,omitempty"`
	Scopes          []string `yaml:"scopes,omitempty"`
	UsernameClaim   string   `yaml:"usernameClaim,omitempty"`
	GroupsClaim     string   `yaml:"groupsClaim,omitempty"`

	// ldap
	URL             string `yaml:"url,omitempty"`
	BindDN          string `yaml:"bindDn,omitempty"`
	BindPasswordEnv string `yaml:"bindPasswordEnv,omitempty"`
	BaseDN          string `yaml:"baseDn,omitempty"`
	UserFilter      string `yaml:"userFilter,omitempty"` // e.g. (uid={username})
	GroupAttr       string `yaml:"groupAttr,omitempty"`  // default memberOf

	// mock-ldap: a YAML file of users. Refused off loopback unless Force.
	File  string `yaml:"file,omitempty"`
	Force bool   `yaml:"force,omitempty"`

	// What a NEW external user is granted on first login. Each value is
	// "superadmin", "project_admin:<project>", "workflow_admin:<workflow>"
	// or "workflow_runner:<workflow>". Unset: no roles.
	DefaultRole string            `yaml:"defaultRole,omitempty"`
	GroupRoles  map[string]string `yaml:"groupRoles,omitempty"`

	// ORGANIZATIONS (oidc / google only; ADR 0035). Unset: nothing changes.
	//
	// OrgClaim names the id_token claim that says which organization
	// (tenant) the person belongs to, e.g. "tid". When it is set, a sign-in
	// without that claim is refused, and the person is bound to one tenant
	// for good: a later sign-in naming another organization is refused.
	OrgClaim string `yaml:"orgClaim,omitempty"`
	// RoleClaim + RoleMap give the person's role IN the organization. The
	// claim's value is only a lookup key into RoleMap, which the operator
	// writes: a value maps to "org-admin" or "member", and a value not in
	// the map (or no claim at all) gives "member". The provider never names
	// a role wfnexus grants by itself.
	RoleClaim string            `yaml:"roleClaim,omitempty"`
	RoleMap   map[string]string `yaml:"roleMap,omitempty"`
	// AutoCreateOrg creates the tenant on its first sign-in. Off, a sign-in
	// naming an organization the operator has not created is refused.
	AutoCreateOrg bool `yaml:"autoCreateOrg,omitempty"`
	// OrgPolicy: "guarded" (the default once orgClaim is set) holds an
	// organization's people to the customer authoring guard; "full" does
	// not. Anything but "full" is read as guarded.
	OrgPolicy string `yaml:"orgPolicy,omitempty"`
}

// The two roles a person can hold in an organization.
const (
	OrgRoleAdmin  = "org-admin"
	OrgRoleMember = "member"
)

// The two organization policies.
const (
	OrgPolicyGuarded = "guarded"
	OrgPolicyFull    = "full"
)

// NormalizeOrg checks the organization settings and fills their defaults
// (orgPolicy "guarded" when orgClaim is set). It runs once, when the
// providers are built, on the copy every later reader sees; a provider it
// refuses is not enabled. Each setting that only means something next to
// orgClaim is refused without it, so a half-written block is a loud error
// rather than a quietly ignored one.
func (p *AuthProvider) NormalizeOrg() error {
	if p.OrgClaim == "" {
		switch {
		case p.RoleClaim != "", len(p.RoleMap) > 0:
			return errors.New("roleClaim and roleMap need orgClaim")
		case p.AutoCreateOrg:
			return errors.New("autoCreateOrg needs orgClaim")
		case p.OrgPolicy != "":
			return errors.New("orgPolicy needs orgClaim")
		}
		return nil
	}
	switch strings.ToLower(p.Type) {
	case "oidc", "google":
	default:
		// The organization is read from a verified id_token; a provider
		// that issues none has nothing trustworthy to read it from.
		return fmt.Errorf("orgClaim needs an OpenID Connect provider (oidc or google), not %q", p.Type)
	}
	// A superadmin sees across every tenant, so a provider that places
	// people in organizations must never make one: that would turn an
	// organization's sign-in into a way out of the organization.
	if grantsSuperadmin(p.DefaultRole) {
		return errors.New("defaultRole grants superadmin, which a provider with orgClaim never may")
	}
	for g, r := range p.GroupRoles {
		if grantsSuperadmin(r) {
			return fmt.Errorf("groupRoles[%q] grants superadmin, which a provider with orgClaim never may", g)
		}
	}
	if len(p.RoleMap) > 0 && p.RoleClaim == "" {
		return errors.New("roleMap needs roleClaim")
	}
	for v, role := range p.RoleMap {
		if role != OrgRoleAdmin && role != OrgRoleMember {
			return fmt.Errorf("roleMap[%q] = %q: a value maps to %q or %q", v, role, OrgRoleAdmin, OrgRoleMember)
		}
	}
	switch p.OrgPolicy {
	case "":
		p.OrgPolicy = OrgPolicyGuarded
	case OrgPolicyGuarded, OrgPolicyFull:
	default:
		return fmt.Errorf("orgPolicy %q: use %q or %q", p.OrgPolicy, OrgPolicyGuarded, OrgPolicyFull)
	}
	return nil
}

// grantsSuperadmin reads a role spec ("superadmin", "project_admin:<p>", ...)
// the way the api package grants it — the part before ":", trimmed,
// lowercased, "-" and " " read as "_" — and says whether it is superadmin.
// The same reading, so no spelling (" SuperAdmin ") slips past this check
// and is still granted.
func grantsSuperadmin(spec string) bool {
	role, _, _ := strings.Cut(strings.TrimSpace(spec), ":")
	role = strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(role)))
	return role == "superadmin"
}
