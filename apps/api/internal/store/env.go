package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The platform's own environment store: what a run gets before the workflow
// file says anything.
//
// Two scopes, and the distinction is about ownership rather than convenience.
// SYSTEM is the platform's — a proxy, a registry, the model key that every run
// uses. PROJECT belongs to one repository, because a token that can push to one
// repo has no business being handed to a workflow from another.

const (
	ScopeSystem  = "system"
	ScopeProject = "project"
)

// EnvVar is one entry as the API returns it: never with its value, when secret.
type EnvVar struct {
	Scope     string `json:"scope"`
	ScopeName string `json:"scopeName,omitempty"`
	Key       string `json:"key"`
	Secret    bool   `json:"secret"`
	// Value is present ONLY for a non-secret. A secret's value leaves the
	// database exactly once, into the process that runs the step.
	Value     string    `json:"value,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Sealer is the encryption the store writes through. It is an interface so the
// store does not import a key: whoever holds the key passes it in.
type Sealer interface {
	Seal(plain string) ([]byte, error)
	Open(sealed []byte) (string, error)
}

// PutEnvVar writes one entry, replacing any previous value under that name.
func (s *Store) PutEnvVar(ctx context.Context, box Sealer, scope, scopeName, key, value string, secret bool) error {
	enc, err := box.Seal(value)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO env_vars (scope, scope_name, key, value_enc, secret, updated_at)
		VALUES ($1,$2,$3,$4,$5, now())
		ON CONFLICT (scope, scope_name, key) DO UPDATE SET
			value_enc=EXCLUDED.value_enc, secret=EXCLUDED.secret, updated_at=now()`,
		scope, scopeName, key, enc, secret)
	return err
}

func (s *Store) DeleteEnvVar(ctx context.Context, scope, scopeName, key string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM env_vars WHERE scope=$1 AND scope_name=$2 AND key=$3`, scope, scopeName, key)
	return err
}

// ListEnvVars returns one scope's entries for display. A secret comes back with
// its name and nothing else — the value is not fetched, not decrypted and not
// serialised, so there is no path by which it reaches a screen or a log.
func (s *Store) ListEnvVars(ctx context.Context, box Sealer, scope, scopeName string) ([]EnvVar, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT scope, scope_name, key, value_enc, secret, updated_at
		FROM env_vars WHERE scope=$1 AND scope_name=$2 ORDER BY key`, scope, scopeName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EnvVar{}
	for rows.Next() {
		var v EnvVar
		var enc []byte
		if err := rows.Scan(&v.Scope, &v.ScopeName, &v.Key, &enc, &v.Secret, &v.UpdatedAt); err != nil {
			return nil, err
		}
		if !v.Secret {
			// Ordinary configuration — a URL, a flag. Hiding it would make the
			// store useless for what it is mostly used for.
			if plain, err := box.Open(enc); err == nil {
				v.Value = plain
			} else {
				v.Value = "(cannot decrypt — wrong key)"
			}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// EnvFor returns a scope's entries as a plain map, decrypted. This is the ONLY
// path a secret's value takes out of the database, and its one caller is the
// engine, about to run a step.
func (s *Store) EnvFor(ctx context.Context, box Sealer, scope, scopeName string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key, value_enc FROM env_vars WHERE scope=$1 AND scope_name=$2`, scope, scopeName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var enc []byte
		if err := rows.Scan(&k, &enc); err != nil {
			return nil, err
		}
		plain, err := box.Open(enc)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		out[k] = plain
	}
	return out, rows.Err()
}

// EnvKeyNames lists the names held in a scope, without touching a value. It is
// what a dry run and the UI use when they only need to say what exists.
func (s *Store) EnvKeyNames(ctx context.Context, scope, scopeName string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key FROM env_vars WHERE scope=$1 AND scope_name=$2 ORDER BY key`, scope, scopeName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

var _ = pgx.ErrNoRows
