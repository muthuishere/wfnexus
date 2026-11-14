package auth

import (
	"context"
	"fmt"
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
	// ApprovingUser is the person a loopback device approval is for, when no
	// one is signed in: an existing loopback-only account, or the install's
	// first account (its owner), created on first use. Any other new name is
	// refused: an approver policy on loopback holds only because a name typed
	// there can never become a new account, let alone one with a role.
	ApprovingUser(ctx context.Context, name string) (*store.User, error)
}

// Local is the only Authenticator today: users and their hashed tokens in the
// server's own database. It is exactly the lookup the handlers used to do
// inline, moved behind the interface unchanged.
type Local struct{ Store *store.Store }

// ApprovingUser creates the install's FIRST user, with the superadmin role:
// on loopback the machine is the trust boundary, and the person at it owns the
// install. Only the first: once there is a user, a new name is refused (an
// admin adds users, who then sign in), so nobody at the machine can mint a
// fresh superadmin to get past an approver policy or prevent_self_approval.
//
// It is for loopback-only accounts: the first one, signed into again later.
// An account with a password, or one an external provider signs in
// (`name@provider`), has a credential of its own; a loopback approval does
// not stand in for it, or anyone at the machine could take that person's
// session (and with it their say as an approver). They sign in with their
// credential.
func (l Local) ApprovingUser(ctx context.Context, name string) (*store.User, error) {
	if strings.Contains(name, "@") {
		return nil, fmt.Errorf("%s signs in through its login provider, not with a loopback approval", name)
	}
	if u, err := l.Store.UserByName(ctx, name); err == nil {
		if u.Disabled {
			return nil, fmt.Errorf("user %s is disabled", name)
		}
		if u.HasPassword {
			return nil, fmt.Errorf("user %s has a password: sign in with it (wfx login --user %s), not with a loopback approval", name, name)
		}
		return u, nil
	}
	u := &store.User{Name: name, DisplayName: name}
	ok, err := l.Store.CreateFirstUser(ctx, u)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no user %s: a loopback approval creates only the first account; an admin adds the others (wfx user add), who then sign in", name)
	}
	return u, nil
}
