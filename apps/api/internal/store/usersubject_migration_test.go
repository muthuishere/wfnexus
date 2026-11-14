package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/muthuishere/wfnexus/apps/api/migrations"
)

// seedBeforeSubjects writes, on a database migrated to just before subjects
// were kept: an external-looking account with a live session/refresh token,
// and a local account with a password and a token.
func seedBeforeSubjects(t *testing.T, st *Store) (ext, local *User) {
	t.Helper()
	ctx := context.Background()
	ext, local = &User{Name: "gus@sso"}, &User{Name: "lee@home"}
	for _, u := range []*User{ext, local} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if _, err := st.IssueUserToken(ctx, u.ID, "hash-"+u.Name, "refresh:browser", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec(`UPDATE users SET password_hash='x' WHERE name='lee@home'`); err != nil {
		t.Fatal(err)
	}
	return ext, local
}

func checkSubjectsMigration(t *testing.T, st *Store, ext, local *User) {
	t.Helper()
	ctx := context.Background()
	if s, _ := st.LinkState(ctx, ext.ID); s != LinkLegacy {
		t.Fatalf("external account state = %q", s)
	}
	if toks, _ := st.ListUserTokens(ctx, ext.ID); len(toks) != 0 {
		t.Fatalf("the external account kept %d tokens", len(toks))
	}
	if s, _ := st.LinkState(ctx, local.ID); s != "" {
		t.Fatalf("a local account with a password was marked %q", s)
	}
	if toks, _ := st.ListUserTokens(ctx, local.ID); len(toks) != 1 {
		t.Fatalf("the local account lost its token: %d", len(toks))
	}
}

// SQLite: the migration marks external-looking accounts and ends their
// sessions, refresh tokens and API tokens; a local account is untouched.
func TestSubjectsMigrationSignsOutLegacyAccountsSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	src, err := iofs.New(migrations.FS, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, sqliteMigrateURL(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(22); err != nil {
		t.Fatal(err)
	}
	m.Close()
	st, err := Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ext, local := seedBeforeSubjects(t, st)
	if err := Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	checkSubjectsMigration(t, st, ext, local)
}

// Postgres: the same, running the migration's SQL on a database whose rows
// are reset to the shape before it.
func TestSubjectsMigrationSignsOutLegacyAccountsPostgres(t *testing.T) {
	st := openPostgres(t)
	if st == nil {
		t.Skip("no postgres")
	}
	ext, local := seedBeforeSubjects(t, st)
	if _, err := st.db.Exec(`UPDATE users SET link_state='', auth_subject='' WHERE name IN ('gus@sso','lee@home')`); err != nil {
		t.Fatal(err)
	}
	sqlText, err := migrations.FS.ReadFile("000030_user_subject.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(string(sqlText)); err != nil {
		t.Fatal(err)
	}
	checkSubjectsMigration(t, st, ext, local)
}
