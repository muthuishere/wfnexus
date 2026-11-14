package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

const linkUsage = `usage:
  wfx-server link list            accounts bound on first sign-in after the upgrade, open to review
  wfx-server link confirm NAME    keep the binding (an account that held roles then signs in with them)
  wfx-server link reject NAME     undo the binding: the account is unbound, signed out, and that
                                  subject may not bind it again; the rightful owner signs in to bind it`

// linkCmd is `wfx-server link …`: run on the server machine against the
// server's own database, with no web sign-in — so the only administrator can
// confirm their own account link after an upgrade. The same from the web, for
// a signed-in superadmin: GET /api/users/links, POST
// /api/users/{name}/link/confirm and /api/users/{name}/link/reject.
func linkCmd(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(linkUsage)
	}
	switch args[0] {
	case "list", "ls":
		links, err := st.PendingLinks(ctx)
		if err != nil {
			return err
		}
		if len(links) == 0 {
			fmt.Fprintln(out, "no account links are open to review")
			return nil
		}
		fmt.Fprintf(out, "%-28s %-9s %-10s %-28s %s\n", "ACCOUNT", "STATE", "PROVIDER", "SIGNED IN AS", "SUBJECT")
		for _, l := range links {
			state := "pending"
			if l.State == store.LinkLinked {
				state = "linked"
			}
			fmt.Fprintf(out, "%-28s %-9s %-10s %-28s %s\n", l.Name, state, l.Provider, l.Claim, l.Subject)
		}
		return nil
	case "confirm", "reject":
		if len(args) != 2 {
			return errors.New(linkUsage)
		}
		do, verb := st.ConfirmLink, "confirmed"
		if args[0] == "reject" {
			do, verb = st.RejectLink, "rejected"
		}
		if err := do(ctx, args[1]); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%s has no account link open to review", args[1])
			}
			return err
		}
		log.Printf("auth: AUDIT account link for %s %s from the server console", args[1], verb)
		fmt.Fprintf(out, "%s: %s\n", verb, args[1])
		return nil
	}
	return errors.New(linkUsage)
}
