package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthProvidersFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `auth:
  providers:
    - name: corp
      type: ldap
      url: ldaps://ldap.example.com
      bindDn: cn=svc,dc=example,dc=com
      bindPasswordEnv: LDAP_BIND_PW
      baseDn: dc=example,dc=com
      userFilter: (uid={username})
      groupRoles: {ops: "workflow_runner:deploy"}
    - name: gh
      type: github
      clientId: abc
      clientSecretEnv: GH_SECRET
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ps := cfg.Auth.Providers
	if len(ps) != 2 || ps[0].BindPasswordEnv != "LDAP_BIND_PW" || ps[0].GroupRoles["ops"] != "workflow_runner:deploy" || ps[1].ClientSecretEnv != "GH_SECRET" {
		t.Fatalf("providers: %+v", ps)
	}
}
