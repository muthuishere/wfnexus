package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store/pgtest"
	"github.com/muthuishere/wfnexus/apps/api/migrations"
)

// The tenant migration's version per dialect. The upgrade test starts from
// the newest version below it that exists in the tree, whatever that is.
const (
	pgTenants     = 32
	sqliteTenants = 26
)

func versionBefore(t *testing.T, dir string, v uint) uint {
	t.Helper()
	ents, err := fs.ReadDir(migrations.FS, dir)
	if err != nil {
		t.Fatal(err)
	}
	var best uint
	for _, e := range ents {
		var n uint
		if _, err := fmt.Sscanf(e.Name(), "%06d_", &n); err == nil && n < v && n > best {
			best = n
		}
	}
	if best == 0 {
		t.Fatalf("no migration below %d in %q", v, dir)
	}
	return best
}

// bothDialects runs check on a fresh SQLite file and on this package's own
// Postgres database. A missing Postgres is a SKIP, reported as one.
func bothDialects(t *testing.T, check func(t *testing.T, st *Store)) {
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "t.db")
		if err := Migrate(driverSQLite, path); err != nil {
			t.Fatal(err)
		}
		st, err := Open(context.Background(), driverSQLite, path)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		check(t, st)
	})
	t.Run("postgres", func(t *testing.T) {
		st := openPostgres(t)
		if st == nil {
			t.Skip("no postgres reachable — run `task infra:up`, or point TEST_DATABASE_URL at a server")
		}
		check(t, st)
	})
}

func migrateTo(t *testing.T, driver, dir, url string, version uint) {
	t.Helper()
	src, err := iofs.New(migrations.FS, dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Migrate(version); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("%s to %d: %v", driver, version, err)
	}
}

// An install that existed before tenants keeps every project, run, bundle and
// member, and all of it lands in 'default'.
func TestTenantMigrationUpgradeKeepsEveryRowInDefault(t *testing.T) {
	upgrade := func(t *testing.T, db *sql.DB, d dialect, up func()) {
		ctx := context.Background()
		q := func(s string, args ...any) {
			t.Helper()
			if _, err := db.ExecContext(ctx, d.q(s), args...); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
		uid := uuid.New()
		q(`INSERT INTO users (id, name, role) VALUES ($1, 'old-user', '')`, uid)
		q(`INSERT INTO members (scope, name, user_id, role) VALUES ('project', 'old-proj', $1, 'project_admin')`, uid)
		for i := 0; i < 3; i++ {
			q(`INSERT INTO workflow_runs (id, project, workflow, status) VALUES ($1, $2, 'wf', 'succeeded')`,
				uuid.New(), fmt.Sprintf("old-proj-%d", i%2))
		}
		q(`INSERT INTO published_bundles (id, kind, project, name, version, digest, manifest) VALUES ($1, 'workflow', 'old-proj-0', 'b', '1.0.0', 'sha256:x', '{}')`, uuid.New())

		up()

		count := func(s string) int {
			t.Helper()
			var n int
			if err := db.QueryRowContext(ctx, d.q(s)).Scan(&n); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
			return n
		}
		if n := count(`SELECT count(*) FROM workflow_runs`); n != 3 {
			t.Fatalf("runs after upgrade: %d, want 3", n)
		}
		if n := count(`SELECT count(*) FROM workflow_runs WHERE tenant_id='default'`); n != 3 {
			t.Fatalf("runs in default after upgrade: %d, want 3", n)
		}
		if n := count(`SELECT count(*) FROM published_bundles WHERE tenant_id='default'`); n != 1 {
			t.Fatalf("bundles in default after upgrade: %d, want 1", n)
		}
		if n := count(`SELECT count(*) FROM members WHERE name='old-proj'`); n != 1 {
			t.Fatalf("members after upgrade: %d, want 1", n)
		}
		if n := count(`SELECT count(*) FROM tenants WHERE id='default'`); n != 1 {
			t.Fatalf("default tenant rows: %d", n)
		}
		// Every pre-existing project now answers 'default', and its runs
		// are readable in it.
		st := &Store{db: db, d: d}
		for _, p := range []string{"old-proj", "old-proj-0", "old-proj-1"} {
			if tn, err := st.ProjectTenant(ctx, p); err != nil || tn != DefaultTenant {
				t.Fatalf("project %s: tenant %q, %v", p, tn, err)
			}
		}
		if rs, err := st.RunsInTenant(ctx, DefaultTenant, 10); err != nil || len(rs) != 3 {
			t.Fatalf("runs readable in default: %d, %v", len(rs), err)
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "u.db")
		u := sqliteMigrateURL(path)
		migrateTo(t, "sqlite", "sqlite", u, versionBefore(t, "sqlite", sqliteTenants))
		st, err := Open(context.Background(), driverSQLite, path)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		upgrade(t, st.db, st.d, func() { migrateTo(t, "sqlite", "sqlite", u, sqliteTenants) })
	})
	t.Run("postgres", func(t *testing.T) {
		dsn, err := pgtest.DSN()
		if err != nil {
			t.Skip("no postgres reachable — run `task infra:up`, or point TEST_DATABASE_URL at a server")
		}
		// A schema of its own, so the package's shared database (already at
		// the newest version) is not touched.
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		schema := "upg_" + uuid.NewString()[:8]
		if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
		pu, _ := url.Parse(dsn)
		qv := pu.Query()
		qv.Set("search_path", schema)
		pu.RawQuery = qv.Encode()
		scoped := pu.String()
		migrateTo(t, "postgres", ".", scoped, versionBefore(t, ".", pgTenants))
		st, err := Open(context.Background(), driverPostgres, scoped)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		var sp string
		if err := st.db.QueryRow(`SELECT current_schema()`).Scan(&sp); err != nil || sp != schema {
			t.Fatalf("not in the throwaway schema: %q %v", sp, err)
		}
		upgrade(t, st.db, st.d, func() { migrateTo(t, "postgres", ".", scoped, pgTenants) })
	})
}

// A fresh install has exactly one tenant, 'default', and a new run lands in it.
func TestTenantFreshInstallWritesDefault(t *testing.T) {
	bothDialects(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		r, err := st.CreateRun(ctx, "fresh-"+uuid.NewString()[:8], "wf", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.RunInTenant(ctx, DefaultTenant, r.ID); err != nil {
			t.Fatalf("a new run of an unbound project is not in default: %v", err)
		}
	})
}

func TestTenantScopedRunReads(t *testing.T) {
	bothDialects(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		sfx := uuid.NewString()[:8]
		ta, tb := "ta-"+sfx, "tb-"+sfx
		pa, pb := "pa-"+sfx, "pb-"+sfx
		for _, id := range []string{ta, tb} {
			if _, err := st.CreateTenant(ctx, id, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.CreateTenant(ctx, ta, ""); !errors.Is(err, ErrTenantExists) {
			t.Fatalf("duplicate tenant: %v", err)
		}
		if _, err := st.CreateTenant(ctx, "Bad_ID", ""); !errors.Is(err, ErrBadTenantID) {
			t.Fatalf("bad tenant id: %v", err)
		}
		// A run created BEFORE its project is bound moves with the bind.
		early, err := st.CreateRun(ctx, pa, "wf", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.BindProject(ctx, ta, pa); err != nil {
			t.Fatal(err)
		}
		if err := st.BindProject(ctx, ta, pa); err != nil {
			t.Fatalf("rebinding to the same tenant: %v", err)
		}
		if err := st.BindProject(ctx, tb, pa); !errors.Is(err, ErrProjectBound) {
			t.Fatalf("moving a project to another tenant: %v", err)
		}
		if err := st.BindProject(ctx, tb, pb); err != nil {
			t.Fatal(err)
		}
		ra, err := st.CreateRun(ctx, pa, "wf", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		rb, err := st.CreateRun(ctx, pb, "wf", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}

		for _, id := range []uuid.UUID{early.ID, ra.ID} {
			if _, err := st.RunInTenant(ctx, ta, id); err != nil {
				t.Fatalf("own run: %v", err)
			}
		}
		if _, err := st.RunInTenant(ctx, ta, rb.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("another tenant's run: %v", err)
		}
		if _, err := st.RunInTenant(ctx, ta, uuid.New()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("absent run: %v", err)
		}
		if _, err := st.RunInTenant(ctx, DefaultTenant, ra.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a bound project's run is visible in default: %v", err)
		}
		list, err := st.RunsInTenant(ctx, ta, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 {
			t.Fatalf("tenant a lists %d runs, want 2", len(list))
		}
		for _, r := range list {
			if r.Project != pa {
				t.Fatalf("tenant a lists a run of %s", r.Project)
			}
		}

		// A bundle published in a bound project is written in its tenant.
		bd := &PublishedBundle{Kind: "workflow", Project: pa, Name: "b-" + sfx, Version: "1.0.0", Digest: "sha256:" + sfx, Manifest: json.RawMessage(`{}`)}
		if err := st.CreateBundle(ctx, bd); err != nil {
			t.Fatal(err)
		}
		var bt string
		if err := st.db.QueryRowContext(ctx, st.d.q(`SELECT tenant_id FROM published_bundles WHERE id=$1`), bd.ID).Scan(&bt); err != nil || bt != ta {
			t.Fatalf("bundle tenant %q, want %q (%v)", bt, ta, err)
		}

		// Fail-closed: a row whose tenant disagrees with its project's
		// binding (a run that raced a bind) is shown to NEITHER tenant.
		if _, err := st.db.ExecContext(ctx, st.d.q(`UPDATE workflow_runs SET tenant_id=$1 WHERE id=$2`), tb, ra.ID); err != nil {
			t.Fatal(err)
		}
		for _, tn := range []string{ta, tb} {
			if _, err := st.RunInTenant(ctx, tn, ra.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a run with a stale tenant is visible to %s: %v", tn, err)
			}
		}
	})
}

func TestServicePrincipalLifecycle(t *testing.T) {
	bothDialects(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		sfx := uuid.NewString()[:8]
		ta, tb := "sa-"+sfx, "sb-"+sfx
		for _, id := range []string{ta, tb} {
			if _, err := st.CreateTenant(ctx, id, ""); err != nil {
				t.Fatal(err)
			}
		}
		tok := newTestServiceToken()
		p, err := st.CreateServicePrincipal(ctx, ta, "svc", HashExposeToken(tok), "wfxs_ab")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateServicePrincipal(ctx, ta, "svc", HashExposeToken(newTestServiceToken()), ""); !errors.Is(err, ErrPrincipalExists) {
			t.Fatalf("duplicate name in a tenant: %v", err)
		}
		if _, err := st.CreateServicePrincipal(ctx, "no-such-"+sfx, "svc", HashExposeToken(newTestServiceToken()), ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("principal in a missing tenant: %v", err)
		}
		// The token value is never stored: only its hash is in the row.
		var stored string
		if err := st.db.QueryRowContext(ctx, st.d.q(`SELECT token_hash FROM service_principals WHERE id=$1`), p.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored == tok || stored != HashExposeToken(tok) {
			t.Fatal("the stored credential is not the hash of the token")
		}
		got, err := st.ServicePrincipalByTokenHash(ctx, HashExposeToken(tok))
		if err != nil || got.ID != p.ID || got.TenantID != ta {
			t.Fatalf("lookup by hash: %+v %v", got, err)
		}
		if err := st.SetServicePrincipalSurface(ctx, ta, p.ID, "https://tools.example.test/mcp", []string{"b.read", "a.write", "a.write"}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetServicePrincipalSurface(ctx, tb, p.ID, "https://x.example.test", nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("surface set through another tenant: %v", err)
		}
		if _, err := st.ServicePrincipalInTenant(ctx, tb, p.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("principal read through another tenant: %v", err)
		}
		got, _ = st.ServicePrincipalInTenant(ctx, ta, p.ID)
		if got.Endpoint != "https://tools.example.test/mcp" || len(got.Tools) != 2 || got.Tools[0] != "a.write" {
			t.Fatalf("surface: %+v", got)
		}
		if err := st.RevokeServicePrincipal(ctx, tb, p.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("revoke through another tenant: %v", err)
		}
		if err := st.RevokeServicePrincipal(ctx, ta, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ServicePrincipalByTokenHash(ctx, HashExposeToken(tok)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a revoked principal still resolves: %v", err)
		}
	})
}

// newTestServiceToken stands in for auth.NewServiceToken (auth imports store,
// so the test cannot import auth). HashExposeToken is the same sha256-hex rule
// as auth.HashToken.
func newTestServiceToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "wfxs_" + hex.EncodeToString(b)
}
