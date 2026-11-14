package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// Many revokes at once, each of which would leave no superadmin if it ran
// alone against a stale count: at least one superadmin always remains.
func revokeRace(t *testing.T, st *Store, rounds, n int) {
	t.Helper()
	ctx := context.Background()
	run := uuid.NewString()[:8]
	for r := 0; r < rounds; r++ {
		// Only this round's admins exist.
		if _, err := st.db.Exec(`UPDATE users SET role='' WHERE role='superadmin'`); err != nil {
			t.Fatal(err)
		}
		var names []string
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("admin-%s-%d-%d@corp", run, r, i)
			if err := st.CreateUser(ctx, &User{Name: name, Role: "superadmin"}); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, name := range names {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				<-start
				if _, err := st.RevokeSuperadminUnlessLast(ctx, name); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}(name)
		}
		close(start)
		wg.Wait()
		var left int
		if err := st.db.QueryRow(`SELECT count(*) FROM users WHERE role='superadmin' AND disabled=false`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 1 {
			t.Fatalf("round %d: %d superadmins left, want exactly 1", r, left)
		}
	}
}

func TestConcurrentRevokesLeaveOneSuperadminSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	if err := Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	revokeRace(t, st, 60, 6)
}

func TestConcurrentRevokesLeaveOneSuperadminPostgres(t *testing.T) {
	st := openPostgres(t)
	if st == nil {
		t.Skip("no postgres")
	}
	revokeRace(t, st, 60, 6)
}
