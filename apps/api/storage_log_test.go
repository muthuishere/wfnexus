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

// The storage line prints host, port and database name, and nothing else
// from the connection string (#172).
func TestTheStorageLineShowsOnlyHostPortAndDatabase(t *testing.T) {
	for _, c := range []struct{ name, dsn, want string }{
		{"url", "postgres://wfx:" + bootPW + "@db.internal:5432/wfnexus?sslmode=require", "host=db.internal port=5432 database=wfnexus"},
		{"url, escaped password", "postgres://wfx:" + bootPW + "%40x@db.internal/wfnexus", "host=db.internal database=wfnexus"},
		{"url, '@' in the password", "postgres://wfx:" + bootPW + "@x@db.internal/wfnexus", "host=db.internal database=wfnexus"},
		{"url, query password", "postgres://wfx@db.internal/wfnexus?password=" + bootPW + "&sslmode=disable", "host=db.internal database=wfnexus"},
		{"url, query sslpassword", "postgres://wfx@db.internal/wfnexus?sslpassword=" + bootPW, "host=db.internal database=wfnexus"},
		{"url, query password with a bad escape", "postgres://wfx@db.internal/wfnexus?password=" + bootPW + "%zz", "host=db.internal database=wfnexus"},
		{"url, query password with ';'", "postgres://wfx@db.internal/wfnexus?password=" + bootPW + ";sslmode=disable", "host=db.internal database=wfnexus"},
		{"key/value", "host=db.internal port=5432 user=wfx password=" + bootPW + " dbname=wfnexus", "host=db.internal port=5432 database=wfnexus"},
		{"key/value sslpassword", "host=db.internal sslpassword=" + bootPW + " dbname=wfnexus", "host=db.internal database=wfnexus"},
		{"key/value, :// in a value", "host=db.internal application_name=a://b password=" + bootPW + " dbname=wfnexus", "host=db.internal database=wfnexus"},
	} {
		for _, driver := range []string{"postgres", "pgx"} {
			out := captureLog(func() { logStorage(driver, c.dsn) })
			if want := "storage: " + driver + " " + c.want + "\n"; !strings.HasSuffix(out, want) {
				t.Errorf("%s (%s): got %q, want suffix %q", c.name, driver, out, want)
			}
			if strings.Contains(out, bootPW) {
				t.Errorf("%s (%s): the storage line printed the password: %q", c.name, driver, out)
			}
		}
	}
	if out := captureLog(func() { logStorage("sqlite", "/data/wfnexus.db") }); !strings.HasSuffix(out, "storage: sqlite /data/wfnexus.db\n") {
		t.Errorf("sqlite: %q", out)
	}
}

// Where a field could hold part of the password, no target is shown at all:
// a numeric password prefix parses as a port, a password tail as the path,
// the query or a fragment.
func TestAnAmbiguousConnectionStringShowsNoTarget(t *testing.T) {
	const prefix = "93715"
	for _, c := range []struct{ name, dsn string }{
		{"'#' after a numeric prefix", "postgres://wfx:" + prefix + "#" + bootPW + "@db.internal/wfnexus"},
		{"'/' after a numeric prefix", "postgres://wfx:" + prefix + "/" + bootPW + "@db.internal/wfnexus"},
		{"'?' after a numeric prefix", "postgres://wfx:" + prefix + "?" + bootPW + "@db.internal/wfnexus"},
		{"does not parse", "postgres://wfx:" + bootPW + "%zz@db.internal/wfnexus"},
		{"key/value, escaped space", `host=db.internal user=wfx password=abc\ ` + bootPW + " dbname=wfnexus"},
		{"key/value, quoted", "host=db.internal password='" + bootPW + " x' dbname=wfnexus"},
		{"key/value, stray token", "host=db.internal password=abc " + bootPW + " dbname=wfnexus"},
		{"a field a name cannot hold", "postgres://wfx@db.internal/wfnexus%2F" + bootPW},
	} {
		got := storageTarget("postgres", c.dsn)
		if got != notShown {
			t.Errorf("%s: target %q, want it not shown", c.name, got)
		}
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
