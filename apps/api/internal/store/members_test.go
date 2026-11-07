package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store/pgtest"
	"github.com/muthuishere/wfnexus/apps/api/migrations"
)

// ADR 0028: the role-rename migration, and workflow members, on BOTH dialects.

// stepMigration moves one dialect's schema by n steps (negative = down).
func stepMigration(t *testing.T, driver, dsn string, n int) {
	t.Helper()
	dir, url := ".", dsn
	if normalizeDriver(driver) == driverSQLite {
		dir, url = "sqlite", sqliteMigrateURL(dsn)
	}
	src, err := iofs.New(migrations.FS, dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Steps(n); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate %d steps: %v", n, err)
	}
}

func TestRolesMigration(t *testing.T) {
	check := func(t *testing.T, driver, dsn string) {
		ctx := context.Background()
		// Back to the schema before 0028, with the OLD role names in use.
		stepMigration(t, driver, dsn, -1)
		st, err := Open(ctx, driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		suffix := uuid.NewString()[:8]
		names := map[string]string{"admin": "a-" + suffix, "publisher": "p-" + suffix, "viewer": "v-" + suffix, "custom": "c-" + suffix}
		for role, name := range names {
			if err := st.CreateUser(ctx, &User{Name: name, Role: role}); err != nil {
				t.Fatal(err)
			}
		}
		st.Close()

		stepMigration(t, driver, dsn, 1)
		st, err = Open(ctx, driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, n := range names {
				_ = st.DeleteUser(ctx, n)
			}
			st.Close()
		})
		// admin -> superadmin now; publisher/viewer are marked for the boot
		// conversion (which needs the workflows); anything else -> none.
		want := map[string]string{"admin": "superadmin", "publisher": "legacy:publisher", "viewer": "legacy:viewer", "custom": ""}
		for old, name := range names {
			u, err := st.UserByName(ctx, name)
			if err != nil || u.Role != want[old] {
				t.Fatalf("%s (was %s) migrated to %+v, %v", name, old, u, err)
			}
		}

		// members exists, upserts, and refuses a role that does not fit its scope.
		u, _ := st.UserByName(ctx, names["viewer"])
		wf, proj := "wf-"+suffix, "p-"+suffix
		if err := st.SetMember(ctx, MemberWorkflow, wf, u.ID, WorkflowRunner); err != nil {
			t.Fatal(err)
		}
		if err := st.SetMember(ctx, MemberWorkflow, wf, u.ID, WorkflowAdmin); err != nil {
			t.Fatal(err)
		}
		if err := st.SetMember(ctx, MemberProject, proj, u.ID, ProjectAdmin); err != nil {
			t.Fatal(err)
		}
		if ms, _ := st.ListMembers(ctx, MemberWorkflow, wf); len(ms) != 1 || ms[0].Role != WorkflowAdmin || ms[0].User != names["viewer"] {
			t.Fatalf("workflow members: %+v", ms)
		}
		if gs, _ := st.GrantsOf(ctx, u.ID); len(gs) != 2 {
			t.Fatalf("grants: %+v", gs)
		}
		for _, bad := range [][2]string{{MemberProject, WorkflowRunner}, {MemberWorkflow, ProjectAdmin}, {MemberWorkflow, "editor"}} {
			if err := st.SetMember(ctx, bad[0], "x-"+suffix, u.ID, bad[1]); err == nil {
				t.Fatalf("stored %s on a %s", bad[1], bad[0])
			}
		}
		if err := st.RemoveMember(ctx, MemberWorkflow, wf, u.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.RemoveMember(ctx, MemberWorkflow, wf, u.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second remove: %v", err)
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "m.db")
		if err := Migrate(driverSQLite, path); err != nil {
			t.Fatal(err)
		}
		check(t, driverSQLite, path)
	})
	t.Run("postgres", func(t *testing.T) {
		if openPostgres(t) == nil {
			t.Skip("no postgres reachable — run `task infra:up`, or point TEST_DATABASE_URL at a server")
		}
		dsn, _ := pgtest.DSN()
		check(t, driverPostgres, dsn)
	})
}
