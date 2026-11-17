package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// THE DATABASE PASSWORD NEVER REACHES THE LOG (#172).
//
// A container's log is collected and kept by whatever runs it, so nothing the
// boot prints may carry the connection string's secrets.
//
//   - The storage line prints the connection string WE hold, redacted
//     structurally (redactDSN).
//   - A Postgres migrate/open error is NEVER printed as the driver wrote it:
//     a URL that does not parse is quoted by the parser, cut at '#', '/' or
//     '?' or %q-escaped, so no replacement of the password can be complete.
//     The boot prints a fixed message, the redacted connection string, and a
//     reason. Only errors whose text is known to hold no connection string
//     are passed through (an allow-list, not a block-list): a dirty migration
//     version, a migration's own failure, and a message the database SERVER
//     sent. Everything else gets a reason picked from the error's kind.
//   - SQLite's connection string is a file path with no secret; its errors
//     print as they are.

// migrateDB and openDB are the store's; a test swaps them to reach the open
// error, which a real broken URL never gets to (the migration fails first).
var (
	migrateDB = store.Migrate
	openDB    = store.Open
)

// openStorage is the boot's storage sequence: migrate, open, name it.
func openStorage(ctx context.Context, driver, dsn string) (*store.Store, error) {
	if err := migrateDB(driver, dsn); err != nil {
		return nil, storageError("migrate", driver, dsn, err)
	}
	st, err := openDB(ctx, driver, dsn)
	if err != nil {
		return nil, storageError("open", driver, dsn, err)
	}
	logStorage(driver, dsn)
	return st, nil
}

// storageError is what the boot prints for a failed step: never the driver's
// raw text for a Postgres connection string.
func storageError(step, driver, dsn string, err error) error {
	if !strings.Contains(strings.ToLower(driver), "postgres") {
		return fmt.Errorf("%s %s: %w", step, driver, err)
	}
	return fmt.Errorf("%s postgres %s: %s", step, redactDSN(dsn), safeReason(err))
}

// safeReason is an allow-listed message for err, or a reason chosen from its
// kind. It never returns the text of an error it does not know.
func safeReason(err error) string {
	var dirty migrate.ErrDirty
	if errors.As(err, &dirty) {
		return dirty.Error()
	}
	server := ""
	var pqErr *pq.Error
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pqErr):
		server = fmt.Sprintf("the database server said: %s (SQLSTATE %s)", pqErr.Message, pqErr.Code)
	case errors.As(err, &pgErr):
		server = fmt.Sprintf("the database server said: %s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
	}
	var mErr migratedb.Error
	var mErrP *migratedb.Error
	if errors.As(err, &mErr) || errors.As(err, &mErrP) {
		if mErrP != nil {
			mErr = *mErrP
		}
		msg := fmt.Sprintf("a migration failed at line %d: %s", mErr.Line, mErr.Err)
		if server != "" {
			msg += "; " + server
		}
		return msg
	}
	if server != "" {
		return server
	}
	var urlErr *url.Error
	var netErr net.Error
	text := strings.ToLower(err.Error()) // read to classify, never printed
	switch {
	case errors.As(err, &urlErr), strings.Contains(text, "parse "), strings.Contains(text, "invalid"):
		return "the connection string does not parse as a URL (check characters in the password: escape them)"
	case strings.Contains(text, "scheme"), strings.Contains(text, "unknown driver"):
		return "the connection string is not a postgres:// URL"
	case errors.As(err, &netErr), strings.Contains(text, "dial "), strings.Contains(text, "connection refused"), strings.Contains(text, "no such host"):
		return "the database cannot be reached at that address"
	}
	return "it failed; the driver's message is withheld because it may contain the connection string"
}

// logStorage names the storage the server opened, never with its password.
func logStorage(driver, dsn string) {
	log.Printf("storage: %s %s", driver, redactDSN(dsn))
}

const hidden = "xxxxx"

var (
	// urlScheme: a connection string is a URL only if it STARTS with a scheme;
	// a key/value value may itself contain "://".
	urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
	// kvSecret is any key ending in "password" (password, sslpassword, …) in a
	// key/value connection string, its value quoted or not.
	kvSecret = regexp.MustCompile(`(?i)(\b\w*password\s*=\s*)('(?:[^'\\]|\\.)*'|\S+)`)
)

// redactDSN is the connection string with every secret replaced. A URL that
// does not parse is hidden whole rather than guessed at. A plain path (sqlite)
// is returned as it is.
func redactDSN(dsn string) string {
	if urlScheme.MatchString(dsn) {
		u, err := url.Parse(dsn)
		if err != nil {
			return "(connection string hidden: it does not parse as a URL)"
		}
		if q := u.Query(); len(q) > 0 {
			for k := range q {
				if strings.HasSuffix(strings.ToLower(k), "password") {
					q.Set(k, hidden)
				}
			}
			u.RawQuery = q.Encode()
		}
		return u.Redacted()
	}
	return kvSecret.ReplaceAllString(dsn, "${1}"+hidden)
}
