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
// keeps the password out of it: the connection string is NEVER printed, in
// any form (raw, redacted or scrubbed), on success or on error. A driver
// accepts many shapes that put the secret somewhere a redaction misses (a
// fragment, a bad-escape query pair, an escaped space), and a parser that
// fails quotes a cut or escaped piece of it.
//
//   - The storage line prints TYPED fields taken from the connection string:
//     host, port and database name, and only when the string is unambiguous
//     (storageTarget). Otherwise it says the target is not shown.
//   - A Postgres migrate/open error is never printed as the driver wrote it:
//     a fixed message, the same typed target, and a reason. Only errors whose
//     text holds no connection string pass through (an allow-list): a dirty
//     migration version, a migration's own line and message (not the error it
//     wraps), and what the database SERVER said.
//   - SQLite's connection string is a file path with no secret; it and its
//     errors print as they are. Every other driver is treated as Postgres.

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
	if store.IsSQLite(driver) {
		return fmt.Errorf("%s %s: %w", step, driver, err)
	}
	return fmt.Errorf("%s %s (%s): %s", step, driver, storageTarget(driver, dsn), safeReason(err))
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
	log.Printf("storage: %s %s", driver, storageTarget(driver, dsn))
}

const notShown = "target not shown: the connection string is ambiguous"

var (
	// urlScheme: a connection string is a URL only if it STARTS with a scheme.
	urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
	// safeField is what a printed host, port or database name may contain.
	safeField = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// kvPair is one key=value in a key/value connection string.
	kvPair = regexp.MustCompile(`^(\w+)=(.*)$`)
)

// storageTarget is what may be printed about where the storage is: for
// SQLite the path; for Postgres only host, port and database name, built from
// typed fields, and only when the connection string is unambiguous. A secret
// that sits where a field is read (a numeric password prefix read as a port,
// a password suffix read as the path) always leaves an '@' or a '#' after
// the host, so those shapes are not shown at all.
func storageTarget(driver, dsn string) string {
	if store.IsSQLite(driver) {
		return dsn
	}
	var host, port, db string
	if urlScheme.MatchString(dsn) {
		u, err := url.Parse(dsn)
		if err != nil {
			return notShown
		}
		// A raw '#' starts a fragment: whatever follows (a password's tail)
		// was cut out of the userinfo.
		if strings.Contains(dsn, "#") {
			return notShown
		}
		// An '@' after the host means the authority ended early, inside the
		// userinfo: the password's tail is in the path or the query.
		if strings.Contains(u.EscapedPath()+"?"+u.RawQuery, "@") {
			return notShown
		}
		host, port, db = u.Hostname(), u.Port(), strings.TrimPrefix(u.Path, "/")
	} else {
		// Key/value: only plain key=value tokens. A quoted or escaped value
		// that holds a space leaves a token with no '=', so it is not shown.
		for _, tok := range strings.Fields(dsn) {
			m := kvPair.FindStringSubmatch(tok)
			if m == nil {
				return notShown
			}
			switch strings.ToLower(m[1]) {
			case "host":
				host = m[2]
			case "port":
				port = m[2]
			case "dbname":
				db = m[2]
			}
		}
	}
	var parts []string
	for _, f := range [][2]string{{"host", host}, {"port", port}, {"database", db}} {
		if f[1] == "" {
			continue
		}
		if !safeField.MatchString(f[1]) {
			return notShown
		}
		parts = append(parts, f[0]+"="+f[1])
	}
	if len(parts) == 0 {
		return notShown
	}
	return strings.Join(parts, " ")
}
