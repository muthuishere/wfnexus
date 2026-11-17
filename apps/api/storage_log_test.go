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

// The boot's storage line names the database but never prints a secret (#172).
func TestTheBootLogNeverPrintsTheDatabasePassword(t *testing.T) {
	for _, c := range []struct {
		name, dsn string
		keep      []string
	}{
		{"url", "postgres://wfx:" + bootPW + "@db.internal:5432/wfnexus?sslmode=require", []string{"db.internal:5432", "/wfnexus", "wfx", "sslmode=require"}},
		{"url with an escaped password", "postgres://wfx:" + bootPW + "%40x@db.internal/wfnexus", []string{"db.internal", "/wfnexus"}},
		{"url with a query password", "postgres://wfx@db.internal/wfnexus?password=" + bootPW + "&sslmode=disable", []string{"db.internal", "password=xxxxx", "sslmode=disable"}},
		{"url with a query sslpassword", "postgres://wfx@db.internal/wfnexus?sslpassword=" + bootPW, []string{"db.internal", "sslpassword=xxxxx"}},
		{"key/value", "host=db.internal port=5432 user=wfx password=" + bootPW + " dbname=wfnexus", []string{"host=db.internal", "dbname=wfnexus", "password=xxxxx"}},
		{"key/value quoted", "host=db.internal password='" + bootPW + " x' dbname=wfnexus", []string{"host=db.internal", "dbname=wfnexus", "password=xxxxx"}},
		{"key/value sslpassword", "host=db.internal sslpassword=" + bootPW + " dbname=wfnexus", []string{"host=db.internal", "sslpassword=xxxxx"}},
		{"key/value with :// in a value", "host=db.internal application_name=a://b password=" + bootPW + " dbname=wfnexus", []string{"application_name=a://b", "password=xxxxx"}},
	} {
		out := captureLog(func() { logStorage("postgres", c.dsn) })
		if strings.Contains(out, bootPW) {
			t.Errorf("%s: the boot log printed the password: %q", c.name, out)
		}
		for _, k := range c.keep {
			if !strings.Contains(out, k) {
				t.Errorf("%s: the boot log lost %q: %q", c.name, k, out)
			}
		}
	}
	// A sqlite path has no password and is printed as it is.
	if out := captureLog(func() { logStorage("sqlite", "/data/wfnexus.db") }); !strings.Contains(out, "storage: sqlite /data/wfnexus.db") {
		t.Errorf("sqlite: %q", out)
	}
}

// A connection string that does not parse is hidden whole, never printed.
func TestAnUnparseableConnectionStringIsHiddenWhole(t *testing.T) {
	got := redactDSN("postgres://wfx:" + bootPW + "%zz@db.internal/wfnexus")
	if strings.Contains(got, bootPW) || !strings.Contains(got, "hidden") {
		t.Fatalf("redactDSN = %q", got)
	}
}

// The REAL boot sequence: a malformed DATABASE_URL fails in the migration,
// whose error quotes the whole URL. What the boot would print carries no
// password, whichever way the URL is broken.
func TestABrokenDatabaseURLFailsBootWithoutItsPassword(t *testing.T) {
	for _, dsn := range []string{
		"postgres://wfx:" + bootPW + "%zz@db.internal:5432/wfnexus",             // bad escape
		"postgres://wfx:" + bootPW + "@db.internal:notaport/wfnexus",            // bad port
		"postgres://wfx:" + bootPW + "@x@db.internal:notaport/wfnexus",          // @ in the password
		"postgres://wfx:" + bootPW + "@127.0.0.1:1/wfnexus?sslmode=disable",     // refused
		"postgres://wfx@127.0.0.1:1/wfnexus?sslmode=disable&password=" + bootPW, // query password, refused
		"host=127.0.0.1 port=1 user=wfx password=" + bootPW + " dbname=wfnexus", // key/value: no scheme
		"host=127.0.0.1 password=" + bootPW + ":tail dbname=wfnexus",            // key/value, ':' in the password
		"postgres://wfx:" + bootPW + "#1@db.internal/wfnexus",                   // '#' cuts the password
		"postgres://wfx:" + bootPW + "/x@db.internal/wfnexus",                   // '/' cuts it
		"postgres://wfx:" + bootPW + "?x@db.internal/wfnexus",                   // '?' cuts it
		"postgres://wfx:" + bootPW + `\x@db.internal/wfnexus`,                   // '\' is %q-escaped
		"postgres://wfx:" + bootPW + `"x@db.internal/wfnexus`,                   // '"' is %q-escaped
	} {
		var err error
		out := captureLog(func() { _, err = openStorage(context.Background(), "postgres", dsn) })
		if err == nil {
			t.Fatalf("%q booted", dsn)
		}
		if strings.Contains(err.Error(), bootPW) || strings.Contains(out, bootPW) {
			t.Errorf("the boot error carries the password: %v / %q", err, out)
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

// The open step's error is never printed raw either. A real broken URL fails
// in the migration first, so the open error is reached with a fake.
func TestAnOpenErrorIsNeverPrintedRaw(t *testing.T) {
	dsn := "postgres://wfx:" + bootPW + "@db.internal/wfnexus"
	swapStorage(t, func(string, string) error { return nil },
		func(context.Context, string, string) (*store.Store, error) {
			return nil, errors.New("cannot open " + dsn + " (" + bootPW[:8] + ")")
		})
	_, err := openStorage(context.Background(), "postgres", dsn)
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

// SQLite's connection string is a path with no secret: its error is kept whole.
func TestASQLiteStorageErrorIsPrintedWhole(t *testing.T) {
	swapStorage(t, func(string, string) error { return errors.New("disk I/O error at /data/w.db") }, openDB)
	_, err := openStorage(context.Background(), "sqlite", "/data/w.db")
	if err == nil || !strings.Contains(err.Error(), "disk I/O error at /data/w.db") {
		t.Fatalf("sqlite error = %v", err)
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
