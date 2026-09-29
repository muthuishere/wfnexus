package store

import (
	"context"
	"testing"
)

// A boot seeds only what is missing, so it can never undo an operator's edit;
// an edit marks the row as no longer the shipped number.
func TestPricesSeedNeverOverwritesAnEdit(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		seed := map[string][2]float64{"claude-sonnet": {3, 15}, "*": {1, 4}}
		if err := st.SeedPrices(ctx, seed); err != nil {
			t.Fatal(err)
		}
		if err := st.PutPrice(ctx, "claude-sonnet", 2.5, 12); err != nil {
			t.Fatal(err)
		}
		if err := st.SeedPrices(ctx, seed); err != nil { // the next boot
			t.Fatal(err)
		}
		rows, err := st.ListPrices(ctx)
		if err != nil || len(rows) != 2 {
			t.Fatalf("rows %v err %v", rows, err)
		}
		for _, r := range rows {
			switch r.Model {
			case "claude-sonnet":
				if r.In != 2.5 || r.Out != 12 || r.Seeded {
					t.Fatalf("edit lost: %+v", r)
				}
			case "*":
				if r.In != 1 || !r.Seeded {
					t.Fatalf("seed row wrong: %+v", r)
				}
			}
		}
		if err := st.DeletePrice(ctx, "claude-sonnet"); err != nil {
			t.Fatal(err)
		}
		if rows, _ := st.ListPrices(ctx); len(rows) != 1 {
			t.Fatalf("delete left %v", rows)
		}
	})
}
