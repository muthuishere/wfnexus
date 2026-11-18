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
//     line (not its message or the error it wraps), and the SQLSTATE code
//     of a database server error with its standard meaning (never the
//     server's message, describeSQLState).

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
	// The server's message is never printed: it is free text, in the
	// server's language, and it quotes names that can hold the password (a
	// password percent-encoded into the user name, or a user name that IS the
	// secret). Only the SQLSTATE code is printed, with its standard meaning.
	code := ""
	var pqErr *pq.Error
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pqErr):
		code = string(pqErr.Code)
	case errors.As(err, &pgErr):
		code = pgErr.Code
	}
	var mErr migratedb.Error
	var mErrP *migratedb.Error
	if errors.As(err, &mErr) || errors.As(err, &mErrP) {
		if mErrP != nil {
			mErr = *mErrP
		}
		msg := fmt.Sprintf("a migration failed at line %d", mErr.Line)
		if code != "" {
			msg += "; " + describeSQLState(code)
		}
		return msg
	}
	if code != "" {
		return describeSQLState(code)
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
// to the SQLite driver (a URL, userinfo, key=value pairs) is not.
func plainPath(driver, dsn string) bool {
	return store.IsSQLite(driver) &&
		!strings.ContainsAny(dsn, "=@ \t\n") &&
		!strings.Contains(strings.ToLower(dsn), "password")
}

// sqlState is a SQLSTATE code as the standard defines it: five characters,
// digits and upper-case letters. Anything else in that field is not printed.
var sqlState = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// sqlStateMeaning is the standard meaning of the codes a boot most often meets.
var sqlStateMeaning = map[string]string{
	"28P01": "password authentication failed",
	"28000": "the user is not authorized",
	"3D000": "the database does not exist",
	"42501": "insufficient privilege",
	"53300": "too many connections",
	"57P03": "the server is not accepting connections yet",
}

// sqlStateClass is the standard meaning of a code's first two characters.
var sqlStateClass = map[string]string{
	"08": "a connection error",
	"28": "an authorization error",
	"3D": "the database does not exist",
	"42": "a syntax or access rule error",
	"53": "the server is out of resources",
	"57": "the server stopped or is not ready",
}

// describeSQLState is what the boot prints for a database server error: the
// code and its standard meaning, never the server's message.
func describeSQLState(code string) string {
	if !sqlState.MatchString(code) {
		return "the database server refused (its message is withheld)"
	}
	meaning, ok := sqlStateMeaning[code]
	if !ok {
		meaning, ok = sqlStateClass[code[:2]]
	}
	if !ok {
		meaning = "an error"
	}
	return fmt.Sprintf("the database server refused: %s (SQLSTATE %s; its message is withheld)", meaning, code)
}
