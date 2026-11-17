package login

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
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
//
// The person is identified by the entry's stable id — entryUUID, or
// objectGUID on Active Directory — and, failing both, by the entry's own
// username attribute (UsernameClaim, default uid), never by what was typed:
// a directory matches names loosely (case), and a username can be reassigned
// to someone else.
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
		[]string{"dn", groupAttr, "entryUUID", "objectGUID", l.uidAttr()}, nil))
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
	return ldapIdentity(l.cfg.Name, l.uidAttr(), username, e.GetAttributeValue(l.uidAttr()),
		e.GetAttributeValue("entryUUID"), e.GetRawAttributeValue("objectGUID"), groups)
}

// ldapIdentity names the person from their directory entry: the stable id
// (entryUUID, else objectGUID) is the key when there is one, and only without
// one is the entry's own username attribute required. The account's name is
// that attribute, or — when an entry with a stable id lacks it — the name the
// person typed, which is then only a label.
func ldapIdentity(provider, uidAttr, typed, canonical, entryUUID string, objectGUID []byte, groups []string) (*Identity, error) {
	subject := ""
	switch {
	case entryUUID != "":
		subject = "entryUUID:" + strings.ToLower(entryUUID)
	case len(objectGUID) > 0:
		subject = "objectGUID:" + hex.EncodeToString(objectGUID)
	case canonical != "":
		subject = "uid:" + canonical
	default:
		return nil, errors.New("ldap: the entry has neither a stable id (entryUUID, objectGUID) nor a " + uidAttr + " attribute to name the person")
	}
	name := canonical
	if name == "" {
		name = typed
	}
	return &Identity{Provider: provider, Subject: subject, LegacySubject: canonical, Username: name, Groups: groups}, nil
}

// uidAttr is the entry attribute that holds the person's username.
func (l *LDAP) uidAttr() string { return or(l.cfg.UsernameClaim, "uid") }

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
		// ID is the entry's stable id (entryUUID); empty: the username is.
		ID string `yaml:"id"`
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
		// A directory matches names regardless of case; the account is the
		// entry's, named as the entry spells it.
		if strings.EqualFold(u.Username, username) && password != "" &&
			subtle.ConstantTimeCompare([]byte(u.Password), []byte(password)) == 1 {
			subject := "uid:" + u.Username
			if u.ID != "" {
				subject = "entryUUID:" + strings.ToLower(u.ID)
			}
			return &Identity{Provider: m.provider, Subject: subject, LegacySubject: u.Username, Username: u.Username, Groups: u.Groups}, nil
		}
	}
	return nil, ErrBadCredentials
}
