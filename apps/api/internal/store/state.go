package store

import (
	"context"
	"database/sql"
	"errors"
)

// The state store: what a workflow REMEMBERS between runs.
//
// Four scopes — step, workflow, project, global — keyed exactly the way
// env.go keys its two, so (scope, scope_name, key) means the same thing in
// both tables and a person learns the vocabulary once.
//
// NOT A SECRET STORE. Values here are PLAINTEXT: no Sealer, no ciphertext, no
// key. env_vars is the sealed one, and a token belongs there. Nothing refuses a
// credential written here — the database simply cannot protect it, and saying
// so is more useful than pretending otherwise.
//
// Every write is an UPSERT in one statement. Two runs of the same workflow
// write concurrently by design (and on SQLite through one serialised
// connection), and a read-modify-write would lose one of them without anybody
// noticing.

// PutState writes one entry, replacing any previous value under that key.
func (s *Store) PutState(ctx context.Context, scope, scopeName, key, value string) error {
	_, err := s.exec(ctx, `
		INSERT INTO state_vars (scope, scope_name, key, value, updated_at)
		VALUES ($1,$2,$3,$4, now())
		ON CONFLICT (scope, scope_name, key) DO UPDATE SET
			value=EXCLUDED.value, updated_at=now()`,
		scope, scopeName, key, value)
	return err
}

// GetState returns one value and whether it was there at all. The second result
// is what lets a caller tell an empty string apart from a key never written.
func (s *Store) GetState(ctx context.Context, scope, scopeName, key string) (string, bool, error) {
	var v string
	err := s.qrow(ctx,
		`SELECT value FROM state_vars WHERE scope=$1 AND scope_name=$2 AND key=$3`,
		scope, scopeName, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) DeleteState(ctx context.Context, scope, scopeName, key string) error {
	_, err := s.exec(ctx,
		`DELETE FROM state_vars WHERE scope=$1 AND scope_name=$2 AND key=$3`, scope, scopeName, key)
	return err
}

// StateFor returns one scope's whole namespace as a map — what a template
// renders from. Never nil, so `{{ .Workflow.x }}` on an empty scope renders
// empty rather than exploding.
func (s *Store) StateFor(ctx context.Context, scope, scopeName string) (map[string]string, error) {
	rows, err := s.query(ctx,
		`SELECT key, value FROM state_vars WHERE scope=$1 AND scope_name=$2`, scope, scopeName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ListStateByPrefix lists a scope's entries whose scope_name starts with a
// prefix. Its one use is the step scope, whose names are '<workflow>/<step>':
// it is how a workflow's page shows every step's memory without first being
// told which steps exist.
func (s *Store) ListStateByPrefix(ctx context.Context, scope, prefix string) ([]StateVar, error) {
	rows, err := s.query(ctx, `
		SELECT scope, scope_name, key, value, updated_at
		FROM state_vars WHERE scope=$1 AND scope_name LIKE $2
		ORDER BY scope_name, key`, scope, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StateVar{}
	for rows.Next() {
		var v StateVar
		if err := rows.Scan(&v.Scope, &v.ScopeName, &v.Key, &v.Value, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListState is the display form: ordered, with the timestamps, for the UI and
// the CLI.
func (s *Store) ListState(ctx context.Context, scope, scopeName string) ([]StateVar, error) {
	rows, err := s.query(ctx, `
		SELECT scope, scope_name, key, value, updated_at
		FROM state_vars WHERE scope=$1 AND scope_name=$2 ORDER BY key`, scope, scopeName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StateVar{}
	for rows.Next() {
		var v StateVar
		if err := rows.Scan(&v.Scope, &v.ScopeName, &v.Key, &v.Value, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
