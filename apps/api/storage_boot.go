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
// A container's log is collected and kept by whatever runs it. The rule that
// keeps the password out of it: nothing taken from a connection string is
// printed, on success or on error. Not the string (raw, redacted or
// scrubbed), and not fields re-parsed out of it: every parser but the
// driver's own disagrees with the driver somewhere (an escaped space, a
// fragment, a bad-escape query pair), and where they disagree a piece of the
// password lands in a "field".
//
//   - The storage line names the driver and nothing else, for every driver.
//   - A storage error prints as the driver wrote it only for a SQLite
//     connection string that is affirmatively a file path (plainPath).
//     Anything else is never printed as the driver wrote it: a fixed message
//     and a reason. Only errors whose text holds no connection string pass
//     through (an allow-list): a dirty migration version, a migration's own
//     line and message (not the error it wraps), and what the database
//     SERVER said, with every quoted name cut at its first ':' (a password
//     percent-encoded into the user name comes back inside the server's
//     message).

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
	if plainPath(driver, dsn) {
		return fmt.Errorf("%s %s: %w", step, driver, err)
	}
	return fmt.Errorf("%s %s: %s", step, driver, safeReason(err))
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
		server = fmt.Sprintf("the database server said: %s (SQLSTATE %s)", trimQuotedNames(pqErr.Message), pqErr.Code)
	case errors.As(err, &pgErr):
		server = fmt.Sprintf("the database server said: %s (SQLSTATE %s)", trimQuotedNames(pgErr.Message), pgErr.Code)
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

// logStorage names the storage the server opened: the driver, nothing else.
func logStorage(driver, dsn string) {
	log.Printf("storage: %s (connection details not shown)", driver)
}

// plainPath reports whether dsn is affirmatively a SQLite file path, whose
// errors may print as they are. A connection string of any other shape given
// to the SQLite driver (a URL, userinfo, key=value pairs, or a percent-encoded
// string, which can hide an encoded "@" or ":" from these checks) is not.
func plainPath(driver, dsn string) bool {
	return store.IsSQLite(driver) &&
		!strings.ContainsAny(dsn, "=@% \t\n") &&
		!strings.Contains(strings.ToLower(dsn), "password")
}

// quotedName is a double-quoted name in a server message ("wfx", "wfnexus").
var quotedName = regexp.MustCompile(`"([^":]*):[^"]*"`)

// trimQuotedNames cuts every quoted name in a server message at its first
// ':', so 'user "wfx:secret"' prints as 'user "wfx:…"'.
func trimQuotedNames(msg string) string {
	return quotedName.ReplaceAllString(msg, `"$1:…"`)
}
