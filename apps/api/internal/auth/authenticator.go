package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Authenticator is the seam between "who is this person" and everything the
// server does with the answer (ADR 0017). The device grant, subject
// resolution and approval all ask this interface, never the users table
// directly, so an OIDC or LDAP backend later is one new implementation rather
// than a hunt through the handlers for every place a user was looked up.
//
// It covers PEOPLE only. Worker tokens are machine identities minted and
// checked by the engine, and stay out of this on purpose: an identity
// provider has no say over which runner may claim a step.
type Authenticator interface {
	// UserByToken resolves a presented user credential to its user and the
	// token row it matched. Any failure is "not a user credential" — the
	// caller may still try it as a worker token.
	UserByToken(ctx context.Context, tok string) (*store.User, *store.UserToken, error)
	// ApprovingUser is the person a loopback device approval is for, when no
	// one is signed in: the named user, created on first use.
	ApprovingUser(ctx context.Context, name string) (*store.User, error)
}

// Local is the only Authenticator today: users and their hashed tokens in the
// server's own database. It is exactly the lookup the handlers used to do
// inline, moved behind the interface unchanged.
type Local struct{ Store *store.Store }

func (l Local) UserByToken(ctx context.Context, tok string) (*store.User, *store.UserToken, error) {
	if l.Store == nil || !strings.HasPrefix(tok, "wfx_") {
		return nil, nil, errors.New("unauthenticated")
	}
	return l.Store.UserByTokenHash(ctx, HashToken(tok))
}

// ApprovingUser gives a first local user the admin role: on loopback the
// machine is the trust boundary, and the person at it owns the install.
func (l Local) ApprovingUser(ctx context.Context, name string) (*store.User, error) {
	if u, err := l.Store.UserByName(ctx, name); err == nil {
		return u, nil
	}
	u := &store.User{Name: name, DisplayName: name, Role: "admin"}
	if err := l.Store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}
