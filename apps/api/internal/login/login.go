// Package login holds the pluggable login providers (ADR 0028).
//
// A provider only AUTHENTICATES: it answers "this is <username> at <provider>,
// in these groups". What that person may do is wfnexus's own decision, made
// from users.role and the members table. Every provider ends in the same
// session (JWT + refresh), minted by the api package.
package login

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// Identity is who a provider says the person is. (Provider, Subject) is the
// stable key a local user is matched on; Username is only the name shown and
// the start of a new user's name, never a key.
type Identity struct {
	Provider string
	Subject  string // the provider's id for the person: the verified id_token sub, GitHub's numeric user id, the LDAP username
	Username string
	Groups   []string
}

// ErrBadCredentials is the one refusal a password provider gives.
var ErrBadCredentials = errors.New("invalid username or password")

// PasswordProvider checks a username and password (ldap, mock-ldap).
type PasswordProvider interface {
	Authenticate(ctx context.Context, username, password string) (*Identity, error)
}

// RedirectProvider is a browser round trip (oidc, google, github). The
// AuthRequest made at start (state, PKCE verifier, nonce) is handed back at
// the callback.
type RedirectProvider interface {
	AuthCodeURL(ctx context.Context, a AuthRequest, redirectURI string) (string, error)
	Exchange(ctx context.Context, code, redirectURI string, a AuthRequest) (*Identity, error)
}

// Provider is one configured provider: exactly one of Password/Redirect set.
type Provider struct {
	Config   config.AuthProvider
	Password PasswordProvider
	Redirect RedirectProvider
}

// Kind is "password" or "redirect", for the UI's sign-in page.
func (p *Provider) Kind() string {
	if p.Redirect != nil {
		return "redirect"
	}
	return "password"
}

// Build turns the config into providers. `password` (local users) needs no
// object and is skipped here. mock-ldap is refused off loopback unless the
// provider says force: true.
func Build(cfgs []config.AuthProvider, loopback bool) (map[string]*Provider, []error) {
	out := map[string]*Provider{}
	var errs []error
	for _, c := range cfgs {
		if c.Name == "" {
			c.Name = c.Type
		}
		if _, dup := out[c.Name]; dup || c.Name == "password" && c.Type != "password" {
			errs = append(errs, fmt.Errorf("auth provider %q is named twice", c.Name))
			continue
		}
		p := &Provider{Config: c}
		switch strings.ToLower(c.Type) {
		case "password", "":
			continue
		case "mock-ldap":
			if !loopback && !c.Force {
				errs = append(errs, fmt.Errorf("auth provider %q: mock-ldap is for dev and tests; refusing it off loopback (set force: true to override)", c.Name))
				continue
			}
			m, err := LoadMockLDAP(c.File)
			if err != nil {
				errs = append(errs, fmt.Errorf("auth provider %q: %w", c.Name, err))
				continue
			}
			m.provider = c.Name
			p.Password = m
		case "ldap":
			p.Password = &LDAP{cfg: c}
		case "oidc", "google", "github":
			o, err := NewOAuth(c)
			if err != nil {
				errs = append(errs, fmt.Errorf("auth provider %q: %w", c.Name, err))
				continue
			}
			p.Redirect = o
		default:
			errs = append(errs, fmt.Errorf("auth provider %q: unknown type %q", c.Name, c.Type))
			continue
		}
		out[c.Name] = p
	}
	return out, errs
}

// secretFrom reads a secret by the NAME of its environment variable.
func secretFrom(envName string) string {
	if envName == "" {
		return ""
	}
	return os.Getenv(envName)
}
