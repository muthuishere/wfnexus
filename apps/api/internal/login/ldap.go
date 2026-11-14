package login

import (
	"context"
	"crypto/subtle"
	"fmt"
	"os"
	"strings"

	"github.com/go-ldap/ldap/v3"
	"gopkg.in/yaml.v3"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// LDAP: bind as the service account, find the user under BaseDN with
// UserFilter ({username} is replaced, escaped), then bind as that DN with the
// person's password. Groups come from GroupAttr (default memberOf); a group DN
// is reduced to its first RDN value (cn=ops,ou=… -> ops).
type LDAP struct{ cfg config.AuthProvider }

func (l *LDAP) Authenticate(_ context.Context, username, password string) (*Identity, error) {
	if username == "" || password == "" {
		return nil, ErrBadCredentials
	}
	conn, err := ldap.DialURL(l.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("ldap: %w", err)
	}
	defer conn.Close()
	if l.cfg.BindDN != "" {
		if err := conn.Bind(l.cfg.BindDN, secretFrom(l.cfg.BindPasswordEnv)); err != nil {
			return nil, fmt.Errorf("ldap service bind: %w", err)
		}
	}
	filter := l.cfg.UserFilter
	if filter == "" {
		filter = "(uid={username})"
	}
	groupAttr := l.cfg.GroupAttr
	if groupAttr == "" {
		groupAttr = "memberOf"
	}
	res, err := conn.Search(ldap.NewSearchRequest(l.cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, 10, false, strings.ReplaceAll(filter, "{username}", ldap.EscapeFilter(username)),
		[]string{"dn", groupAttr}, nil))
	if err != nil || len(res.Entries) != 1 {
		return nil, ErrBadCredentials
	}
	e := res.Entries[0]
	if err := conn.Bind(e.DN, password); err != nil {
		return nil, ErrBadCredentials
	}
	var groups []string
	for _, g := range e.GetAttributeValues(groupAttr) {
		groups = append(groups, firstRDN(g))
	}
	return &Identity{Provider: l.cfg.Name, Subject: username, Username: username, Groups: groups}, nil
}

func firstRDN(dn string) string {
	if d, err := ldap.ParseDN(dn); err == nil && len(d.RDNs) > 0 && len(d.RDNs[0].Attributes) > 0 {
		return d.RDNs[0].Attributes[0].Value
	}
	return dn
}

// MockLDAP is an in-process fake directory for dev and tests: a YAML file of
// users. It speaks no protocol; it answers what LDAP would.
//
//	users:
//	  - username: ana
//	    password: a-long-password
//	    groups: [ops]
type MockLDAP struct {
	provider string
	Users    []struct {
		Username string   `yaml:"username"`
		Password string   `yaml:"password"`
		Groups   []string `yaml:"groups"`
	} `yaml:"users"`
}

func LoadMockLDAP(path string) (*MockLDAP, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mock-ldap file: %w", err)
	}
	m := &MockLDAP{}
	if err := yaml.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("mock-ldap file: %w", err)
	}
	return m, nil
}

func (m *MockLDAP) Authenticate(_ context.Context, username, password string) (*Identity, error) {
	for _, u := range m.Users {
		if u.Username == username && password != "" &&
			subtle.ConstantTimeCompare([]byte(u.Password), []byte(password)) == 1 {
			return &Identity{Provider: m.provider, Subject: username, Username: username, Groups: u.Groups}, nil
		}
	}
	return nil, ErrBadCredentials
}
