// Package pgtest gives each test package its OWN Postgres database, created on
// first use and dropped when the package's tests finish.
//
// The tests used to fall back to the shared development database (`bfp`)
// whenever TEST_DATABASE_URL was unset. Two sessions working on two branches
// then migrated the same database to different versions, and one branch's
// tests skipped with "no migration found for version 15" — twice, the second
// time because an exported variable does not survive between shell calls, so
// the fallback was taken without anyone choosing it. A database nobody else
// can see makes that impossible rather than merely unlikely.
//
// TEST_DATABASE_URL (or WFX_TEST_POSTGRES) now names the SERVER to create the
// throwaway database on; its database name is ignored. Nothing is ever
// migrated, written or deleted in a database this package did not create.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultServer = "postgres://bfp:bfp@127.0.0.1:5460/postgres?sslmode=disable"

var (
	created string // the database this process made, or ""
	dsn     string
	failure error
	done    bool
)

// DSN returns a connection string for this test package's own database,
// creating it on the first call. An error means there is no Postgres to talk
// to (or no right to create a database on it); callers skip on it.
func DSN() (string, error) {
	if !done {
		done = true
		dsn, failure = create()
	}
	return dsn, failure
}

// Main runs a package's tests and then drops the database DSN created. Every
// package that calls DSN needs it as its TestMain, or the database outlives
// the run.
func Main(m *testing.M) {
	code := m.Run()
	drop()
	os.Exit(code)
}

func server() string {
	for _, k := range []string{"TEST_DATABASE_URL", "WFX_TEST_POSTGRES"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return defaultServer
}

// on returns the server URL pointed at another database on the same server.
func on(db string) (string, error) {
	u, err := url.Parse(server())
	if err != nil {
		return "", fmt.Errorf("pgtest: the server URL does not parse: %w", err)
	}
	u.Path = "/" + db
	return u.String(), nil
}

func admin() (*sql.DB, error) {
	// The maintenance database: it exists on every server, and connecting to
	// it touches nothing anyone works in.
	a, err := on("postgres")
	if err != nil {
		return nil, err
	}
	return sql.Open("pgx", a)
}

func create() (string, error) {
	db, err := admin()
	if err != nil {
		return "", err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return "", fmt.Errorf("pgtest: no postgres (task infra:up): %w", err)
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	// Generated, never user input, so it is safe to put straight into the DDL
	// (a database name cannot be a bind parameter).
	name := fmt.Sprintf("wfx_test_%d_%s", os.Getpid(), hex.EncodeToString(b))
	if _, err := db.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		return "", fmt.Errorf("pgtest: cannot create a throwaway database: %w", err)
	}
	created = name
	return on(name)
}

func drop() {
	if created == "" {
		return
	}
	db, err := admin()
	if err != nil {
		return
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// FORCE: a pool the tests forgot to close must not leave the database
	// behind (Postgres 13+).
	if _, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+created+" WITH (FORCE)"); err != nil {
		fmt.Fprintf(os.Stderr, "pgtest: could not drop %s: %v\n", created, err)
	}
}
