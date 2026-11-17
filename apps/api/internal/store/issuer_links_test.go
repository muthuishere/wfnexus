package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIssuerLinksAndRevocations(t *testing.T) {
	bothDialects(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		sfx := uuid.NewString()[:8]
		ta, tb := "la-"+sfx, "lb-"+sfx
		for _, id := range []string{ta, tb} {
			if _, err := st.CreateTenant(ctx, id, ""); err != nil {
				t.Fatal(err)
			}
		}
		iss, ext := "https://issuer.example.test", "ext-"+sfx
		if _, err := st.IssuerTenant(ctx, iss, ext); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unlinked: %v", err)
		}
		if err := st.LinkIssuerTenant(ctx, ta, iss, ext); err != nil {
			t.Fatal(err)
		}
		if err := st.LinkIssuerTenant(ctx, ta, iss, ext); err != nil {
			t.Fatalf("relink same tenant: %v", err)
		}
		if err := st.LinkIssuerTenant(ctx, tb, iss, ext); !errors.Is(err, ErrLinkExists) {
			t.Fatalf("link to a second tenant: %v", err)
		}
		if got, err := st.IssuerTenant(ctx, iss, ext); err != nil || got != ta {
			t.Fatalf("tenant %q %v", got, err)
		}
		// The same external id at another issuer is a different link.
		if _, err := st.IssuerTenant(ctx, "https://other.example.test", ext); !errors.Is(err, ErrNotFound) {
			t.Fatalf("issuer not part of the key: %v", err)
		}
		if _, ok, _ := st.IssuerRevokedBefore(ctx, iss, ext, "a"); ok {
			t.Fatal("revoked before any revocation")
		}
		at := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
		if err := st.RevokeIssuerSubject(ctx, iss, ext, "a", at); err != nil {
			t.Fatal(err)
		}
		if err := st.RevokeIssuerSubject(ctx, iss, ext, "a", at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, ok, err := st.IssuerRevokedBefore(ctx, iss, ext, "a")
		if err != nil || !ok || !got.Equal(at.Add(time.Hour)) {
			t.Fatalf("revoked before: %v %v %v", got, ok, err)
		}
		if _, ok, _ := st.IssuerRevokedBefore(ctx, iss, ext, "b"); ok {
			t.Fatal("revocation leaked to another subject")
		}
	})
}
