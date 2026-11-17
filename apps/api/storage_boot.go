package main

import (
	"context"
	"errors"
	"log"
	"net/url"
	"regexp"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// THE DATABASE PASSWORD NEVER REACHES THE LOG (#172).
//
// A container's log is collected and kept by whatever runs it, so nothing the
// boot prints may carry the connection string's secrets: not the storage line,
// and not an error. A migration that cannot parse the URL quotes the whole
// URL in its error, so errors are scrubbed too, by VALUE: every secret found
// in the connection string (and its escaped forms) is replaced wherever it
// appears, whatever the message around it looks like.

// openStorage is the boot's storage sequence: migrate, open, name it. Every
// error it returns has already been scrubbed of the connection string's secrets.
func openStorage(ctx context.Context, driver, dsn string) (*store.Store, error) {
	if err := store.Migrate(driver, dsn); err != nil {
		return nil, errors.New("migrate: " + scrubDSN(err.Error(), dsn))
	}
	st, err := store.Open(ctx, driver, dsn)
	if err != nil {
		return nil, errors.New(driver + ": " + scrubDSN(err.Error(), dsn))
	}
	logStorage(driver, dsn)
	return st, nil
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
				if isSecretKey(k) {
					q.Set(k, hidden)
				}
			}
			u.RawQuery = q.Encode()
		}
		return u.Redacted()
	}
	return kvSecret.ReplaceAllString(dsn, "${1}"+hidden)
}

// scrubDSN removes the connection string's secrets from any text (an error).
func scrubDSN(text, dsn string) string {
	if dsn != "" {
		text = strings.ReplaceAll(text, dsn, redactDSN(dsn))
	}
	return scrubSecrets(text, dsnSecrets(dsn))
}

func scrubSecrets(text string, secrets []string) string {
	for _, s := range secrets {
		text = strings.ReplaceAll(text, s, hidden)
	}
	return text
}

func isSecretKey(k string) bool { return strings.HasSuffix(strings.ToLower(k), "password") }

// dsnSecrets is every secret value in a connection string, in the raw and
// escaped forms it may be printed in. It works on strings that do not parse:
// the URL userinfo is cut out by hand.
func dsnSecrets(dsn string) []string {
	var raw []string
	if urlScheme.MatchString(dsn) {
		rest := dsn[strings.Index(dsn, "://")+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			if _, pw, ok := strings.Cut(rest[:at], ":"); ok {
				raw = append(raw, pw)
			}
		}
		if _, query, ok := strings.Cut(dsn, "?"); ok {
			for _, pair := range strings.Split(query, "&") {
				if k, v, ok := strings.Cut(pair, "="); ok && isSecretKey(k) {
					raw = append(raw, v)
				}
			}
		}
	} else {
		for _, m := range kvSecret.FindAllStringSubmatch(dsn, -1) {
			raw = append(raw, m[2], strings.Trim(m[2], "'"))
		}
	}
	var out []string
	for _, s := range raw {
		forms := []string{s, url.QueryEscape(s), url.PathEscape(s)}
		if u, err := url.QueryUnescape(s); err == nil {
			forms = append(forms, u)
		}
		if u, err := url.PathUnescape(s); err == nil {
			forms = append(forms, u)
		}
		for _, f := range forms {
			// A one- or two-character "secret" would scrub ordinary text.
			if len(f) >= 3 {
				out = append(out, f)
			}
		}
	}
	return out
}
