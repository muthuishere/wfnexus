package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/lib/pq"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

const bootPW = "s3cret-boot-pw-7731"

func captureLog(f func()) string {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	f()
	return buf.String()
}

// The storage line prints nothing taken from a Postgres connection string,
// whatever its shape (#172). Shapes where a re-parse would disagree with the
// driver are included: a libpq escaped space keeps "dbname=…" inside the
// password, a numeric password prefix parses as a port.
func TestTheStorageLineShowsNoConnectionDetail(t *testing.T) {
	dsns := []string{
		"postgres://wfx:" + bootPW + "@db.internal:5432/wfnexus?sslmode=require",
		"postgres://wfx:" + bootPW + "%40x@db.internal/wfnexus",
		"postgres://wfx@db.internal/wfnexus?password=" + bootPW + "%zz",
		"postgres://wfx@db.internal/wfnexus?password=" + bootPW + ";sslmode=disable",
		"postgres://wfx:93715#" + bootPW + "@db.internal/wfnexus",
		"host=db.internal port=5432 user=wfx password=" + bootPW + " dbname=wfnexus",
		`host=db.internal user=wfx password=abc\ dbname=` + bootPW,
		`host=db.internal user=wfx password=abc\ port=` + bootPW,
		`host=db.internal user=wfx password=abc\ host=` + bootPW,
	}
	for _, dsn := range dsns {
		for _, driver := range []string{"postgres", "postgresql", "pgx"} {
			out := captureLog(func() { logStorage(driver, dsn) })
			if want := "storage: " + driver + " (connection details not shown)\n"; !strings.HasSuffix(out, want) || strings.Contains(out, bootPW) {
				t.Errorf("%s: got %q, want suffix %q", driver, out, want)
			}
		}
	}
	// Not even a SQLite path: the line names the driver and nothing else.
	if out := captureLog(func() { logStorage("sqlite", "/data/wfnexus.db") }); !strings.HasSuffix(out, "storage: sqlite (connection details not shown)\n") {
		t.Errorf("sqlite: %q", out)
	}
}

// A connection string given to the SQLite driver prints its error whole only
// when it is affirmatively a file path. Any other shape (the two settings are
// separate, so a Postgres string can land here) is not printed, on the line
// or in an error.
func TestOnlyAPlainSQLitePathPrintsItsError(t *testing.T) {
	for _, c := range []struct{ name, dsn string }{
		{"url with userinfo", "postgres://wfx:" + bootPW + "@db.internal/wfnexus"},
		{"bare userinfo", "wfx:" + bootPW + "@db.internal"},
		{"url, query password", "postgres://db.internal/wfnexus?password=" + bootPW},
		{"key/value", "host=db.internal user=wfx password=" + bootPW + " dbname=wfnexus"},
		{"whitespace", "db.internal wfx " + bootPW},
		{"a 'password' in the name", "/tmp/password" + bootPW},
		{"percent-encoded userinfo", "postgres://wfx%3A" + bootPW + "%40db.internal/wfnexus"},
	} {
		if out := captureLog(func() { logStorage("sqlite", c.dsn) }); strings.Contains(out, bootPW) {
			t.Errorf("%s: line %q", c.name, out)
		}
		d := c.dsn
		swapStorage(t, func(string, string) error { return errors.New("unable to open database file " + d) }, openDB)
		_, err := openStorage(context.Background(), "sqlite", c.dsn)
		if err == nil || strings.Contains(err.Error(), bootPW) {
			t.Errorf("%s: error %v", c.name, err)
		}
	}
}

// A user name holding a percent-encoded ':' carries the password into the
// server's own message; every quoted name in it is cut at the ':'.
func TestAServerMessageNeverEchoesAPasswordInTheUserName(t *testing.T) {
	dsn := "postgres://wfx%3A" + bootPW + "@db.internal/wfnexus"
	e := &pq.Error{Code: "28P01", Message: `password authentication failed for user "wfx:` + bootPW + `"`}
	swapStorage(t, func(string, string) error { return e }, openDB)
	_, err := openStorage(context.Background(), "postgres", dsn)
	if err == nil || strings.Contains(err.Error(), bootPW) || !strings.Contains(err.Error(), `password authentication failed for user "wfx:…" (SQLSTATE 28P01)`) {
		t.Fatalf("error = %v", err)
	}
}

// swapStorage replaces the store's migrate and open for one test.
func swapStorage(t *testing.T, m func(string, string) error, o func(context.Context, string, string) (*store.Store, error)) {
	t.Helper()
	om, oo := migrateDB, openDB
	migrateDB, openDB = m, o
	t.Cleanup(func() { migrateDB, openDB = om, oo })
}

// The REAL boot sequence, for every driver name that opens Postgres: a broken
// DATABASE_URL fails in the migration, whose error quotes the URL. What the
// boot would print carries no part of the password.
func TestABrokenDatabaseURLFailsBootWithoutItsPassword(t *testing.T) {
	for _, dsn := range []string{
		"postgres://wfx:" + bootPW + "%zz@db.internal:5432/wfnexus",
		"postgres://wfx:" + bootPW + "@db.internal:notaport/wfnexus",
		"postgres://wfx:" + bootPW + "@x@db.internal:notaport/wfnexus",
		"postgres://wfx:" + bootPW + "@127.0.0.1:1/wfnexus?sslmode=disable",
		"postgres://wfx@127.0.0.1:1/wfnexus?sslmode=disable&password=" + bootPW,
		"host=127.0.0.1 port=1 user=wfx password=" + bootPW + " dbname=wfnexus",
		"host=127.0.0.1 password=" + bootPW + ":tail dbname=wfnexus",
		"postgres://wfx:" + bootPW + "#1@db.internal/wfnexus",
		"postgres://wfx:" + bootPW + "/x@db.internal/wfnexus",
		"postgres://wfx:" + bootPW + "?x@db.internal/wfnexus",
		"postgres://wfx:" + bootPW + `\x@db.internal/wfnexus`,
		"postgres://wfx:" + bootPW + `"x@db.internal/wfnexus`,
	} {
		for _, driver := range []string{"postgres", "postgresql", "pgx", "PGX"} {
			var err error
			out := captureLog(func() { _, err = openStorage(context.Background(), driver, dsn) })
			if err == nil {
				t.Fatalf("%q (%s) booted", dsn, driver)
			}
			if strings.Contains(err.Error(), bootPW[:8]) || strings.Contains(out, bootPW[:8]) {
				t.Errorf("%s: the boot error carries the password: %v / %q", driver, err, out)
			}
		}
	}
}

// The open step's error is never printed raw either. A real broken URL fails
// in the migration first, so the open error is reached with a fake.
func TestAnOpenErrorIsNeverPrintedRaw(t *testing.T) {
	dsn := "postgres://wfx:" + bootPW + "@db.internal/wfnexus"
	swapStorage(t, func(string, string) error { return nil },
		func(context.Context, string, string) (*store.Store, error) {
			return nil, errors.New("cannot open " + dsn + " (" + bootPW[:8] + ")")
		})
	_, err := openStorage(context.Background(), "pgx", dsn)
	if err == nil || strings.Contains(err.Error(), bootPW[:8]) || !strings.Contains(err.Error(), "withheld") {
		t.Fatalf("open error = %v", err)
	}
	// A Postgres error is never printed raw, even when its connection string
	// looks like a plain path.
	swapStorage(t, func(string, string) error { return errors.New("raw driver text") }, openDB)
	if _, err := openStorage(context.Background(), "postgres", "/data/wfnexus"); err == nil || strings.Contains(err.Error(), "raw driver text") {
		t.Fatalf("postgres error with a path-like string = %v", err)
	}
}

// Errors known to hold no connection string keep their message: a dirty
// migration version and what the database SERVER said.
func TestAllowListedStorageErrorsKeepTheirMessage(t *testing.T) {
	dsn := "postgres://wfx:" + bootPW + "@db.internal/wfnexus"
	for _, c := range []struct {
		err  error
		want string
	}{
		{migrate.ErrDirty{Version: 7}, "Dirty database version 7"},
		{&pq.Error{Code: "28P01", Message: `password authentication failed for user "wfx"`}, `the database server said: password authentication failed for user "wfx" (SQLSTATE 28P01)`},
	} {
		e := c.err
		swapStorage(t, func(string, string) error { return e }, openDB)
		_, err := openStorage(context.Background(), "postgres", dsn)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), bootPW) {
			t.Errorf("%T: error = %v", c.err, err)
		}
	}
}

// A migration's own failure keeps its line and message, but not the error it
// wraps, which the driver wrote.
func TestAFailedMigrationKeepsItsLineButNotTheWrappedError(t *testing.T) {
	dsn := "postgres://wfx:" + bootPW + "@db.internal/wfnexus"
	failed := migratedb.Error{Line: 3, Err: "migration failed", OrigErr: errors.New("driver said " + dsn)}
	swapStorage(t, func(string, string) error { return failed }, openDB)
	_, err := openStorage(context.Background(), "postgres", dsn)
	if err == nil || !strings.Contains(err.Error(), "a migration failed at line 3: migration failed") || strings.Contains(err.Error(), bootPW) {
		t.Fatalf("migration error = %v", err)
	}
}

// SQLite's connection string is a path with no secret: its error is kept whole.
func TestASQLiteStorageErrorIsPrintedWhole(t *testing.T) {
	swapStorage(t, func(string, string) error { return errors.New("disk I/O error at /data/w.db") }, openDB)
	_, err := openStorage(context.Background(), "sqlite", "/data/w.db")
	if err == nil || !strings.Contains(err.Error(), "disk I/O error at /data/w.db") {
		t.Fatalf("sqlite error = %v", err)
	}
}
