package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
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

// An error may print the password decoded ("@") where the URL holds it
// escaped ("%40"), or the other way round: both forms are scrubbed.
func TestAnErrorIsScrubbedOfEveryFormOfThePassword(t *testing.T) {
	dsn := "postgres://wfx:" + bootPW + "%40x@db.internal/wfnexus"
	for _, text := range []string{
		"auth failed for password " + bootPW + "@x",
		"auth failed for password " + bootPW + "%40x",
	} {
		if got := scrubDSN(text, dsn); strings.Contains(got, bootPW) {
			t.Errorf("scrubDSN(%q) = %q", text, got)
		}
	}
}
