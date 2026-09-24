package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// The state store, against a real database — SQLite always, Postgres when one
// is reachable (WFX_TEST_POSTGRES, or `task infra:up`'s default dsn).
//
// The interesting properties are not "it stores a string". They are: the four
// scopes are four separate namespaces, and a concurrent writer does not lose a
// write. Both are things a read-modify-write implementation passes in a single
// goroutine and fails in production.

func eachStore(t *testing.T, fn func(t *testing.T, st *Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, openSQLite(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := openPostgres(t)
		if st == nil {
			t.Skip("no postgres reachable — set WFX_TEST_POSTGRES or run `task infra:up`")
		}
		fn(t, st)
	})
}

// openPostgres returns nil when there is nothing to talk to, so the suite still
// runs on a laptop with no infrastructure.
func openPostgres(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("WFX_TEST_POSTGRES")
	if dsn == "" {
		dsn = "postgres://bfp:bfp@127.0.0.1:5460/bfp?sslmode=disable"
	}
	if err := Migrate(driverPostgres, dsn); err != nil {
		return nil
	}
	st, err := Open(context.Background(), driverPostgres, dsn)
	if err != nil {
		return nil
	}
	t.Cleanup(func() {
		// Leave the shared database as it was found.
		_, _ = st.db.Exec(`DELETE FROM state_vars WHERE scope_name LIKE 'test-%' OR key LIKE 'test-%'`)
		st.Close()
	})
	return st
}

func TestStateReadAfterWrite(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		if _, ok, err := st.GetState(ctx, model.StateScopeWorkflow, "test-wf", "test-cursor"); err != nil || ok {
			t.Fatalf("a key never written must report absent: ok=%v err=%v", ok, err)
		}
		if err := st.PutState(ctx, model.StateScopeWorkflow, "test-wf", "test-cursor", "4120"); err != nil {
			t.Fatal(err)
		}
		v, ok, err := st.GetState(ctx, model.StateScopeWorkflow, "test-wf", "test-cursor")
		if err != nil || !ok || v != "4120" {
			t.Fatalf("read after write: %q ok=%v err=%v", v, ok, err)
		}
		// A second write REPLACES rather than failing on the primary key.
		if err := st.PutState(ctx, model.StateScopeWorkflow, "test-wf", "test-cursor", "4121"); err != nil {
			t.Fatal(err)
		}
		m, err := st.StateFor(ctx, model.StateScopeWorkflow, "test-wf")
		if err != nil || m["test-cursor"] != "4121" {
			t.Fatalf("upsert did not replace: %v %v", m, err)
		}
		if err := st.DeleteState(ctx, model.StateScopeWorkflow, "test-wf", "test-cursor"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := st.GetState(ctx, model.StateScopeWorkflow, "test-wf", "test-cursor"); ok {
			t.Fatal("delete left the key behind")
		}
	})
}

// The whole point of four scopes: the same key in each is four different
// values, and none of them falls back to another.
func TestStateScopesDoNotCollide(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		addr := map[string]string{
			model.StateScopeStep:     "test-wf/scan",
			model.StateScopeWorkflow: "test-wf",
			model.StateScopeProject:  "test-proj-a",
			model.StateScopeGlobal:   "",
		}
		for scope, name := range addr {
			if err := st.PutState(ctx, scope, name, "test-k", "value-"+scope); err != nil {
				t.Fatal(err)
			}
		}
		for scope, name := range addr {
			v, ok, err := st.GetState(ctx, scope, name, "test-k")
			if err != nil || !ok || v != "value-"+scope {
				t.Fatalf("%s scope read %q (ok=%v), want value-%s", scope, v, ok, scope)
			}
		}

		// Project A cannot see project B.
		if err := st.PutState(ctx, model.StateScopeProject, "test-proj-b", "test-k", "b-only"); err != nil {
			t.Fatal(err)
		}
		if v, _, _ := st.GetState(ctx, model.StateScopeProject, "test-proj-a", "test-k"); v != "value-project" {
			t.Fatalf("project A read project B's value: %q", v)
		}

		// Step `scan` is invisible to step `report`.
		if _, ok, _ := st.GetState(ctx, model.StateScopeStep, "test-wf/report", "test-k"); ok {
			t.Fatal("one step's state leaked into another's")
		}

		// And nothing falls back: a key only the workflow scope holds is
		// absent from the step scope, not inherited from it.
		if err := st.PutState(ctx, model.StateScopeWorkflow, "test-wf", "test-only-wf", "x"); err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{model.StateScopeStep, model.StateScopeProject, model.StateScopeGlobal} {
			if _, ok, _ := st.GetState(ctx, scope, addr[scope], "test-only-wf"); ok {
				t.Fatalf("%s state fell back to the workflow scope", scope)
			}
		}
	})
}

// Two runs of the same workflow write the same namespace at once. With a
// read-modify-write, one of the writes disappears; with an upsert, the last one
// wins and every write lands somewhere.
func TestStateConcurrentWriters(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		const writers = 16
		var wg sync.WaitGroup
		errs := make(chan error, writers*4)
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Each writer hammers the SAME key (last write wins, nobody
				// errors) and writes a key of its OWN (every one must survive).
				for r := 0; r < 4; r++ {
					if err := st.PutState(ctx, model.StateScopeWorkflow, "test-race", "test-shared", fmt.Sprint(i)); err != nil {
						errs <- err
						return
					}
				}
				if err := st.PutState(ctx, model.StateScopeWorkflow, "test-race", fmt.Sprintf("test-own-%02d", i), "ok"); err != nil {
					errs <- err
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent write failed: %v", err)
		}
		m, err := st.StateFor(ctx, model.StateScopeWorkflow, "test-race")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < writers; i++ {
			if m[fmt.Sprintf("test-own-%02d", i)] != "ok" {
				t.Fatalf("writer %d's key was lost: %v", i, m)
			}
		}
		if m["test-shared"] == "" {
			t.Fatal("the contended key holds nothing")
		}
	})
}

func TestStateListing(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		for _, k := range []string{"test-b", "test-a"} {
			if err := st.PutState(ctx, model.StateScopeStep, "test-wf/scan", k, k+"!"); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.PutState(ctx, model.StateScopeStep, "test-other/scan", "test-c", "no"); err != nil {
			t.Fatal(err)
		}
		vars, err := st.ListState(ctx, model.StateScopeStep, "test-wf/scan")
		if err != nil || len(vars) != 2 || vars[0].Key != "test-a" {
			t.Fatalf("listing is not the scope's keys in order: %+v %v", vars, err)
		}
		if vars[0].UpdatedAt.IsZero() {
			t.Fatal("updated_at did not come back as a time")
		}
		byPrefix, err := st.ListStateByPrefix(ctx, model.StateScopeStep, "test-wf/")
		if err != nil || len(byPrefix) != 2 {
			t.Fatalf("prefix listing crossed into another workflow: %+v %v", byPrefix, err)
		}
	})
}
