package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// CreateFirstUser makes exactly one superadmin however many first sign-ins
// race for it, on both dialects; once there is a user it creates nothing.
func TestCreateFirstUserMakesExactlyOneOwner(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		// Postgres is this package's shared test database: start from no
		// users and leave none behind.
		if !st.d.isSQLite() {
			_, _ = st.db.ExecContext(ctx, `DELETE FROM users`)
			t.Cleanup(func() { _, _ = st.db.ExecContext(context.Background(), `DELETE FROM users`) })
		}
		const n = 8
		var wg sync.WaitGroup
		oks := make([]bool, n)
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				oks[i], errs[i] = st.CreateFirstUser(ctx, &User{Name: fmt.Sprintf("first-%d", i)})
			}(i)
		}
		wg.Wait()
		won := 0
		for i := range oks {
			if errs[i] != nil {
				t.Fatalf("sign-in %d: %v", i, errs[i])
			}
			if oks[i] {
				won++
			}
		}
		users, _ := st.ListUsers(ctx)
		if won != 1 || len(users) != 1 || users[0].Role != "superadmin" {
			t.Fatalf("%d sign-ins won, users %+v; want exactly one superadmin", won, users)
		}
		if ok, err := st.CreateFirstUser(ctx, &User{Name: "later"}); ok || err != nil {
			t.Fatalf("a later first user: ok=%v err=%v", ok, err)
		}
	})
}
