package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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

func TestRoleRenameMigration(t *testing.T) {
	check := func(t *testing.T, driver, dsn string) {
		ctx := context.Background()
		// Back to the schema before 0028, with the OLD role names in use.
		stepMigration(t, driver, dsn, -1)
		st, err := Open(ctx, driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		suffix := uuid.NewString()[:8]
		for name, perms := range map[string][]string{
			"admin": {"*"}, "publisher": {"workflows:write"}, "viewer": {"runs:read"},
		} {
			if err := st.UpsertRole(ctx, name, perms); err != nil {
				t.Fatal(err)
			}
		}
		names := map[string]string{"admin": "a-" + suffix, "publisher": "p-" + suffix, "viewer": "v-" + suffix}
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
		want := map[string]string{"admin": "superadmin", "publisher": "editor", "viewer": "user"}
		for old, name := range names {
			u, err := st.UserByName(ctx, name)
			if err != nil || u.Role != want[old] {
				t.Fatalf("%s (was %s) migrated to %+v, %v", name, old, u, err)
			}
		}
		if p, err := st.Role(ctx, "superadmin"); err != nil || !slices.Contains(p, "*") {
			t.Fatalf("superadmin role: %v %v", p, err)
		}
		if p, err := st.Role(ctx, "user"); err != nil || !slices.Contains(p, "runs:write") || !slices.Contains(p, "runs:read") {
			t.Fatalf("user role kept its perms and gained runs:write: %v %v", p, err)
		}
		if p, err := st.Role(ctx, "editor"); err != nil || !slices.Contains(p, "runs:all") || !slices.Contains(p, "workflows:write") {
			t.Fatalf("editor role: %v %v", p, err)
		}
		if _, err := st.Role(ctx, "admin"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the old admin role survived: %v", err)
		}

		// workflow_members exists and behaves.
		u, _ := st.UserByName(ctx, names["viewer"])
		wf := "wf-" + suffix
		if err := st.SetWorkflowMember(ctx, wf, u.ID, "editor"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetWorkflowMember(ctx, wf, u.ID, "owner"); err != nil {
			t.Fatal(err)
		}
		if r, _ := st.WorkflowMemberRole(ctx, wf, u.ID); r != "owner" {
			t.Fatalf("member role = %q, want owner (upsert)", r)
		}
		if ms, _ := st.ListWorkflowMembers(ctx, wf); len(ms) != 1 || ms[0].User != names["viewer"] {
			t.Fatalf("members: %+v", ms)
		}
		if err := st.SetWorkflowMember(ctx, wf, u.ID, "god"); err == nil {
			t.Fatal("an unknown member role was stored")
		}
		if err := st.RemoveWorkflowMember(ctx, wf, u.ID); err != nil {
			t.Fatal(err)
		}
		if r, _ := st.WorkflowMemberRole(ctx, wf, u.ID); r != "" {
			t.Fatalf("removed member still has %q", r)
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
