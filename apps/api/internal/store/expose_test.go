package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// Exposure tokens on both databases: hashed at rest, one active per target,
// rotation and revocation kill the old plaintext, idempotency keys claim once.
func TestExposeTokensStore(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		p := "test-exp-" + uuid.NewString()[:8]
		tok, secret, err := st.IssueExposeToken(ctx, p, "wf", ExposeHook)
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.LookupExposeToken(ctx, secret)
		if err != nil || got.ID != tok.ID || got.Workflow != "wf" {
			t.Fatalf("lookup = %+v %v", got, err)
		}
		// Rotation: the old plaintext stops working.
		_, rotated, err := st.IssueExposeToken(ctx, p, "wf", ExposeHook)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.LookupExposeToken(ctx, secret); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a rotated-away token still resolves: %v", err)
		}
		wf := "wf"
		if act, _ := st.ActiveExposeTokens(ctx, p, &wf); len(act) != 1 {
			t.Fatalf("active after rotate = %d, want 1", len(act))
		}
		if n, _ := st.RevokeExposeTokens(ctx, p, "wf", ExposeHook); n != 1 {
			t.Fatalf("revoked %d", n)
		}
		if _, err := st.LookupExposeToken(ctx, rotated); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a revoked token still resolves: %v", err)
		}

		claimed, _, err := st.ReserveIdempotencyKey(ctx, p, "wf", "k1")
		if err != nil || !claimed {
			t.Fatalf("first claim = %v %v", claimed, err)
		}
		if err := st.SetIdempotencyRun(ctx, p, "wf", "k1", "run-1"); err != nil {
			t.Fatal(err)
		}
		claimed, run, err := st.ReserveIdempotencyKey(ctx, p, "wf", "k1")
		if err != nil || claimed || run != "run-1" {
			t.Fatalf("second claim = %v %q %v", claimed, run, err)
		}
	})
}
