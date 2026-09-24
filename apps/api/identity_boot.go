package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/muthuishere/wfnexus/apps/api/internal/api"
	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// bootstrapIdentity decides, before anything is served, whether this process is
// allowed to serve at all (ADR 0017, design §4.6).
//
// The failure mode it exists to prevent, named: an operator sets WFX_ADDR=:8090
// in a container — the manifests already do — no users exist, and the server
// serves an unauthenticated API to the network. A config mistake must not be
// able to produce that, so a non-loopback bind with zero users REFUSES to
// serve and prints the one credential needed to claim the install.
//
// Two honest gaps this cannot see, documented rather than defended:
//   - a reverse proxy in front of a loopback bind re-exposes an unauthenticated
//     API; that is the operator's own tunnel and no bind-address rule can see it.
//   - 127.0.0.1 is shared by every local user and process, so "loopback" means
//     "this machine is the trust boundary". That is the single-machine premise,
//     stated rather than assumed.
func bootstrapIdentity(ctx context.Context, addr string, st *store.Store) error {
	if api.LoopbackOnly(addr) {
		// Absent, not permissive: no user row, no default credential, nothing
		// to log into.
		log.Printf("auth: ABSENT — bound to %s, so this machine is the trust boundary (ADR 0017)", addr)
		return nil
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	if n > 0 {
		log.Printf("auth: REQUIRED — bound to %s, %d user(s) configured; every /api request resolves a subject", addr, n)
		return nil
	}
	return refuseAndBootstrap(ctx, addr, st)
}

// refuseAndBootstrap mints the first administrator and stops. The value is
// printed to stdout — the operator's own terminal — exactly once and is stored
// only as a hash; there is no route that can read it back. The next boot sees a
// user and serves with authentication required.
func refuseAndBootstrap(ctx context.Context, addr string, st *store.Store) error {
	// Roles are ROWS, editable, never a set baked into the binary (ADR 0017).
	// The vocabulary is named from the routes that exist (api/authz.go); this
	// seeds it once, and the table is the authority from then on.
	for name, perms := range api.DefaultRoles() {
		if err := st.UpsertRole(ctx, name, perms); err != nil {
			return fmt.Errorf("identity: seeding role %s: %w", name, err)
		}
	}
	admin := &store.User{Name: "admin", DisplayName: "admin", Role: "admin"}
	if err := st.CreateUser(ctx, admin); err != nil {
		return fmt.Errorf("identity: creating the first admin: %w", err)
	}
	value := auth.NewToken()
	if _, err := st.IssueUserToken(ctx, admin.ID, auth.HashToken(value), "bootstrap", ""); err != nil {
		return fmt.Errorf("identity: issuing the bootstrap credential: %w", err)
	}
	// stdout, not the log: a log line is shipped somewhere, and this is the one
	// moment the value exists outside a client.
	fmt.Fprintf(os.Stdout, `
REFUSING TO SERVE — %s is not a loopback address and this install has no users.

An unauthenticated API on a reachable address is the one thing this server will
not do. An administrator has been created and this is the only time its
credential is shown:

    admin token: %s

Do this, on the machine you want to administer from:

    wfx login --url http://%s        # then approve with the token above
    # or, to use it directly:
    export WFX_API=http://%s

Then start the server again: it will serve with authentication required.

`, addr, value, addr, addr)
	return errors.New("refusing to serve: non-loopback bind with no users (an admin has just been created; restart to serve)")
}
