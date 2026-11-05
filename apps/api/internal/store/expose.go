package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Exposure tokens (ADR 0027): the credential an outside caller presents to
// start ONE workflow through its hook or MCP endpoint. Only the sha256 of a
// token is stored; the plaintext exists once, in the answer that issued it.

const (
	ExposeHook = "hook"
	ExposeMCP  = "mcp"
)

// ExposeToken is one issued token. Workflow "" is a project-wide token.
type ExposeToken struct {
	ID         string     `json:"id"`
	Project    string     `json:"project"`
	Workflow   string     `json:"workflow"`
	Kind       string     `json:"kind"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// HashExposeToken is the stored form of a token, and the HMAC key a caller
// derives to sign a hook body (hex of sha256(token)).
func HashExposeToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newExposeSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "wfxe_" + hex.EncodeToString(b), nil
}

// IssueExposeToken revokes every active token for (project, workflow, kind)
// and issues a new one. It returns the plaintext — the only time it exists.
func (s *Store) IssueExposeToken(ctx context.Context, project, workflow, kind string) (ExposeToken, string, error) {
	if kind != ExposeHook && kind != ExposeMCP {
		return ExposeToken{}, "", errors.New("kind must be hook or mcp")
	}
	if _, err := s.RevokeExposeTokens(ctx, project, workflow, kind); err != nil {
		return ExposeToken{}, "", err
	}
	secret, err := newExposeSecret()
	if err != nil {
		return ExposeToken{}, "", err
	}
	t := ExposeToken{ID: uuid.NewString(), Project: project, Workflow: workflow, Kind: kind,
		Prefix: secret[:12], CreatedAt: time.Now().UTC()}
	_, err = s.exec(ctx, `INSERT INTO expose_tokens (id, project, workflow, kind, token_hash, prefix, created_ns)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t.ID, project, workflow, kind, HashExposeToken(secret), t.Prefix, t.CreatedAt.UnixNano())
	if err != nil {
		return ExposeToken{}, "", err
	}
	return t, secret, nil
}

// RevokeExposeTokens revokes every active token for (project, workflow, kind).
func (s *Store) RevokeExposeTokens(ctx context.Context, project, workflow, kind string) (int64, error) {
	res, err := s.exec(ctx, `UPDATE expose_tokens SET revoked_ns=$1
		WHERE project=$2 AND workflow=$3 AND kind=$4 AND revoked_ns=0`,
		time.Now().UnixNano(), project, workflow, kind)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const exposeCols = `id, project, workflow, kind, prefix, created_ns, revoked_ns, last_used_ns`

func scanExpose(rows interface{ Scan(...any) error }) (ExposeToken, error) {
	var t ExposeToken
	var c, r, u int64
	if err := rows.Scan(&t.ID, &t.Project, &t.Workflow, &t.Kind, &t.Prefix, &c, &r, &u); err != nil {
		return t, err
	}
	t.CreatedAt = time.Unix(0, c).UTC()
	if r != 0 {
		v := time.Unix(0, r).UTC()
		t.RevokedAt = &v
	}
	if u != 0 {
		v := time.Unix(0, u).UTC()
		t.LastUsedAt = &v
	}
	return t, nil
}

// ActiveExposeTokens lists the active tokens of a project, optionally narrowed
// to one workflow ("" for the project token itself is a real value, so a nil
// workflow means "all").
func (s *Store) ActiveExposeTokens(ctx context.Context, project string, workflow *string) ([]ExposeToken, error) {
	q := `SELECT ` + exposeCols + ` FROM expose_tokens WHERE project=$1 AND revoked_ns=0`
	args := []any{project}
	if workflow != nil {
		q += ` AND workflow=$2`
		args = append(args, *workflow)
	}
	rows, err := s.query(ctx, q+` ORDER BY workflow, kind`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExposeToken{}
	for rows.Next() {
		t, err := scanExpose(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LookupExposeToken finds the ACTIVE token whose plaintext is given, or
// ErrNotFound. It compares hashes in SQL; the hash of a 256-bit secret leaks
// nothing useful through timing.
func (s *Store) LookupExposeToken(ctx context.Context, token string) (ExposeToken, error) {
	return s.lookupExposeHash(ctx, HashExposeToken(strings.TrimSpace(token)))
}

func (s *Store) lookupExposeHash(ctx context.Context, hash string) (ExposeToken, error) {
	rows, err := s.query(ctx, `SELECT `+exposeCols+` FROM expose_tokens WHERE token_hash=$1 AND revoked_ns=0`, hash)
	if err != nil {
		return ExposeToken{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return ExposeToken{}, err
		}
		return ExposeToken{}, ErrNotFound
	}
	return scanExpose(rows)
}

// ActiveExposeHashes returns token id → stored hash for the active tokens that
// could authorize (project, workflow, kind): the workflow's own, plus the
// project token. HMAC verification needs the key, which is the hash.
func (s *Store) ActiveExposeHashes(ctx context.Context, project, workflow, kind string) (map[string]string, error) {
	rows, err := s.query(ctx, `SELECT id, token_hash FROM expose_tokens
		WHERE project=$1 AND kind=$2 AND revoked_ns=0 AND (workflow=$3 OR workflow='')`, project, kind, workflow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = h
	}
	return out, rows.Err()
}

// TouchExposeToken records a use. Best effort.
func (s *Store) TouchExposeToken(ctx context.Context, id string) {
	_, _ = s.exec(ctx, `UPDATE expose_tokens SET last_used_ns=$1 WHERE id=$2`, time.Now().UnixNano(), id)
}

// ReserveIdempotencyKey claims key for (project, workflow). When it was already
// claimed it returns the run id recorded for it ("" while the first request is
// still starting its run) and claimed=false.
func (s *Store) ReserveIdempotencyKey(ctx context.Context, project, workflow, key string) (claimed bool, runID string, err error) {
	_, err = s.exec(ctx, `INSERT INTO hook_idempotency (project, workflow, key, created_ns) VALUES ($1,$2,$3,$4)`,
		project, workflow, key, time.Now().UnixNano())
	if err == nil {
		return true, "", nil
	}
	rows, qerr := s.query(ctx, `SELECT run_id FROM hook_idempotency WHERE project=$1 AND workflow=$2 AND key=$3`,
		project, workflow, key)
	if qerr != nil {
		return false, "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, "", err // the insert failed for some other reason
	}
	if err := rows.Scan(&runID); err != nil {
		return false, "", err
	}
	return false, runID, nil
}

// SetIdempotencyRun records the run a claimed key started.
func (s *Store) SetIdempotencyRun(ctx context.Context, project, workflow, key, runID string) error {
	_, err := s.exec(ctx, `UPDATE hook_idempotency SET run_id=$1 WHERE project=$2 AND workflow=$3 AND key=$4`,
		runID, project, workflow, key)
	return err
}

// ReleaseIdempotencyKey forgets a claim whose run never started, so a retry
// with the same key can try again.
func (s *Store) ReleaseIdempotencyKey(ctx context.Context, project, workflow, key string) {
	_, _ = s.exec(ctx, `DELETE FROM hook_idempotency WHERE project=$1 AND workflow=$2 AND key=$3 AND run_id=''`,
		project, workflow, key)
}
