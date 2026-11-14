package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// adminRace makes n superadmins and has every one of them removed at once,
// each by a different path — role sync's revoke, an admin's demote, disable
// or delete — on the given handles (more than one handle: separate
// connections, as two processes would have). At least one enabled superadmin
// must always remain.
func adminRace(t *testing.T, handles []*Store, rounds, n int) {
	t.Helper()
	ctx := context.Background()
	st := handles[0]
	run := uuid.NewString()[:8]
	ops := []func(s *Store, name string) error{
		func(s *Store, name string) error { _, err := s.RevokeSuperadminUnlessLast(ctx, name); return err },
		func(s *Store, name string) error {
			_, err := s.UpdateUserUnlessLastAdmin(ctx, name, "", "", "", false)
			return err
		},
		func(s *Store, name string) error {
			_, err := s.UpdateUserUnlessLastAdmin(ctx, name, "", "superadmin", "", true)
			return err
		},
		func(s *Store, name string) error { return s.DeleteUserUnlessLastAdmin(ctx, name) },
	}
	for r := 0; r < rounds; r++ {
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
		for i, name := range names {
			wg.Add(1)
			go func(i int, name string) {
				defer wg.Done()
				<-start
				err := ops[i%len(ops)](handles[i%len(handles)], name)
				if err != nil && !errors.Is(err, ErrLastAdmin) {
					t.Errorf("%s: %v", name, err)
				}
			}(i, name)
		}
		close(start)
		wg.Wait()
		var left int
		if err := st.db.QueryRow(`SELECT count(*) FROM users WHERE role='superadmin' AND disabled=false`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 1 {
			t.Fatalf("round %d: %d enabled superadmins left, want exactly 1", r, left)
		}
	}
}

func sqliteStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestConcurrentAdminRemovalsLeaveOneSuperadminSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	if err := Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	adminRace(t, []*Store{sqliteStore(t, path)}, 60, 8)
}

// Two handles on one SQLite file are two connection pools, as two processes
// sharing the file would have: BEGIN IMMEDIATE is what serialises them.
func TestConcurrentAdminRemovalsAcrossTwoSQLiteHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	if err := Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	adminRace(t, []*Store{sqliteStore(t, path), sqliteStore(t, path)}, 60, 8)
}

func TestConcurrentAdminRemovalsLeaveOneSuperadminPostgres(t *testing.T) {
	st := openPostgres(t)
	if st == nil {
		t.Skip("no postgres")
	}
	adminRace(t, []*Store{st}, 60, 8)
}

// A confirm and a reject of the same link at once: exactly one wins, and the
// account ends in the winner's state — a confirmed link is never undone.
func linkRace(t *testing.T, st *Store, rounds int) {
	t.Helper()
	ctx := context.Background()
	run := uuid.NewString()[:8]
	for r := 0; r < rounds; r++ {
		u := &User{Name: fmt.Sprintf("link-%s-%d@sso", run, r)}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(st.d.q(`UPDATE users SET link_state='legacy' WHERE id=$1`), u.ID); err != nil {
			t.Fatal(err)
		}
		if ok, err := st.BindLegacy(ctx, u.ID, "sso", "s-"+run, "x", LinkLinked); err != nil || !ok {
			t.Fatalf("bind: %v %v", ok, err)
		}
		var wg sync.WaitGroup
		var confirmErr, rejectErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; confirmErr = st.ConfirmLink(ctx, u.Name) }()
		go func() { defer wg.Done(); <-start; rejectErr = st.RejectLink(ctx, u.Name) }()
		close(start)
		wg.Wait()
		if (confirmErr == nil) == (rejectErr == nil) {
			t.Fatalf("round %d: confirm %v, reject %v: exactly one must win", r, confirmErr, rejectErr)
		}
		state, _ := st.LinkState(ctx, u.ID)
		bound, _ := st.UserBySubject(ctx, "sso", "s-"+run)
		if confirmErr == nil && (state != "" || bound == nil) {
			t.Fatalf("round %d: confirmed, then state %q bound %v", r, state, bound != nil)
		}
		if rejectErr == nil && (state != LinkLegacy || bound != nil) {
			t.Fatalf("round %d: rejected, then state %q bound %v", r, state, bound != nil)
		}
		if bound != nil {
			// Free the subject for the next round.
			_ = st.DeleteUser(ctx, u.Name)
		}
	}
}

func TestConfirmAndRejectRaceSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	if err := Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	linkRace(t, sqliteStore(t, path), 40)
}

func TestConfirmAndRejectRacePostgres(t *testing.T) {
	st := openPostgres(t)
	if st == nil {
		t.Skip("no postgres")
	}
	linkRace(t, st, 40)
}

// The case-insensitive sign-in lookups use an index, not a scan of users.
func TestCaseInsensitiveLookupsUseAnIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	if err := Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st := sqliteStore(t, path)
	for _, q := range []string{
		`SELECT id FROM users WHERE lower(name)=lower('Ana@corp') AND link_state='legacy'`,
		`SELECT id FROM users WHERE auth_provider='corp' AND lower(auth_subject)=lower('Ana')`,
	} {
		rows, err := st.db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, notused int
			var detail string
			_ = rows.Scan(&id, &parent, &notused, &detail)
			plan.WriteString(detail + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), "USING INDEX") {
			t.Errorf("%s:\n%s", q, plan.String())
		}
	}
}
