package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ProjectTokenPrefix starts every project service token, so a bearer is
// routed to this table without trying the others.
const ProjectTokenPrefix = "wfxp_"

// ProjectToken is one issued service token. The plaintext is never stored.
type ProjectToken struct {
	ID         string     `json:"id"`
	Project    string     `json:"project"`
	Label      string     `json:"label"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// ErrNoToken is a token that does not exist, is revoked, or belongs to
// another project — one answer for all three.
var ErrNoToken = errors.New("no such token")

// IssueProjectToken mints a service token for one project. It returns the
// plaintext — the only time it exists.
func (s *Store) IssueProjectToken(ctx context.Context, project, label string) (ProjectToken, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ProjectToken{}, "", err
	}
	secret := ProjectTokenPrefix + hex.EncodeToString(b)
	t := ProjectToken{ID: uuid.NewString(), Project: project, Label: label, Prefix: secret[:13], CreatedAt: time.Now().UTC()}
	_, err := s.exec(ctx, `INSERT INTO project_tokens (id, project, label, token_hash, prefix, created_ns)
		VALUES ($1,$2,$3,$4,$5,$6)`, t.ID, project, label, HashExposeToken(secret), t.Prefix, t.CreatedAt.UnixNano())
	if err != nil {
		return ProjectToken{}, "", err
	}
	return t, secret, nil
}

const projectTokenCols = `id, project, label, prefix, created_ns, revoked_ns, last_used_ns`

func scanProjectToken(row rowScanner) (ProjectToken, error) {
	var t ProjectToken
	var c, r, u int64
	if err := row.Scan(&t.ID, &t.Project, &t.Label, &t.Prefix, &c, &r, &u); err != nil {
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

// LookupProjectToken resolves a presented plaintext to its ACTIVE token.
func (s *Store) LookupProjectToken(ctx context.Context, plaintext string) (ProjectToken, error) {
	t, err := scanProjectToken(s.qrow(ctx, `SELECT `+projectTokenCols+` FROM project_tokens
		WHERE token_hash=$1 AND revoked_ns=0`, HashExposeToken(plaintext)))
	if err != nil {
		return ProjectToken{}, ErrNoToken
	}
	_, _ = s.exec(ctx, `UPDATE project_tokens SET last_used_ns=$2 WHERE id=$1`, t.ID, time.Now().UnixNano())
	return t, nil
}

// ListProjectTokens lists a project's active tokens, newest first.
func (s *Store) ListProjectTokens(ctx context.Context, project string) ([]ProjectToken, error) {
	rows, err := s.query(ctx, `SELECT `+projectTokenCols+` FROM project_tokens
		WHERE project=$1 AND revoked_ns=0 ORDER BY created_ns DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectToken{}
	for rows.Next() {
		t, err := scanProjectToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeProjectToken revokes one token OF THIS PROJECT. A token of another
// project is ErrNoToken, exactly as one that never existed.
func (s *Store) RevokeProjectToken(ctx context.Context, project, id string) error {
	res, err := s.exec(ctx, `UPDATE project_tokens SET revoked_ns=$3
		WHERE id=$1 AND project=$2 AND revoked_ns=0`, id, project, time.Now().UnixNano())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoToken
	}
	return nil
}

// RevokeAllProjectTokens revokes every active token of a project (on delete).
func (s *Store) RevokeAllProjectTokens(ctx context.Context, project string) error {
	_, err := s.exec(ctx, `UPDATE project_tokens SET revoked_ns=$2 WHERE project=$1 AND revoked_ns=0`,
		project, time.Now().UnixNano())
	return err
}
