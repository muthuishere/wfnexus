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
  wfx-server link list            accounts whose link waits for confirmation
  wfx-server link confirm NAME    confirm one (the account then signs in with its roles)`

// linkCmd is `wfx-server link …`: run on the server machine against the
// server's own database, with no web sign-in — so the only administrator can
// confirm their own account link after an upgrade. Confirming from the web is
// POST /api/users/{name}/link/confirm, for a signed-in superadmin.
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
			fmt.Fprintln(out, "no account links are waiting for confirmation")
			return nil
		}
		fmt.Fprintf(out, "%-32s %-12s %s\n", "ACCOUNT", "PROVIDER", "SUBJECT")
		for _, l := range links {
			fmt.Fprintf(out, "%-32s %-12s %s\n", l.Name, l.Provider, l.Subject)
		}
		return nil
	case "confirm":
		if len(args) != 2 {
			return errors.New(linkUsage)
		}
		if err := st.ConfirmLink(ctx, args[1]); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%s has no link waiting for confirmation", args[1])
			}
			return err
		}
		log.Printf("auth: AUDIT account link for %s confirmed from the server console", args[1])
		fmt.Fprintf(out, "confirmed: %s signs in with its roles from now on\n", args[1])
		return nil
	}
	return errors.New(linkUsage)
}
