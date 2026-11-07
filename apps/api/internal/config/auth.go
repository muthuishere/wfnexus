package config

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
}
