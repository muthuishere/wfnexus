package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The published-bundle index (ADR 0018). The BYTES are in the blob store; this
// is only what has to be queried — identity, provenance and the manifest.

// ErrVersionExists is the immutability refusal. It is its own error so a
// handler can answer 409 and say the version exists, rather than flattening
// every write failure into a 500.
var ErrVersionExists = errors.New("version already published")

type PublishedBundle struct {
	ID       uuid.UUID       `json:"id"`
	Kind     string          `json:"kind"`
	Project  string          `json:"project"`
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	Digest   string          `json:"digest"`
	Manifest json.RawMessage `json:"manifest"`
	// Provenance, filled from the AUTHENTICATED SUBJECT and never from a
	// request body. PublishedByName is denormalised so provenance survives the
	// user row being deleted — an audit that forgets who acted is not an audit.
	PublishedBy     *uuid.UUID `json:"publishedBy,omitempty"`
	PublishedByName string     `json:"publishedBy_name"`
	PublishedAt     time.Time  `json:"publishedAt"`
}

const bundleCols = `id, kind, project, name, version, digest, manifest, published_by, published_by_name, published_at`

func scanBundle(row rowScanner) (*PublishedBundle, error) {
	b := &PublishedBundle{}
	var by uuid.NullUUID
	err := row.Scan(&b.ID, &b.Kind, &b.Project, &b.Name, &b.Version, &b.Digest,
		rawJSON{&b.Manifest}, &by, &b.PublishedByName, &b.PublishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if by.Valid {
		id := by.UUID
		b.PublishedBy = &id
	}
	return b, nil
}

// CreateBundle writes the index row. The unique constraint on
// (project, kind, name, version) is what makes a republish a REFUSAL rather
// than an overwrite, so the rule is enforced by the schema and not by a check
// a second caller could forget.
func (s *Store) CreateBundle(ctx context.Context, b *PublishedBundle) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	err := s.qrow(ctx, `
		INSERT INTO published_bundles (id, kind, project, name, version, digest, manifest, published_by, published_by_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9) RETURNING published_at`,
		b.ID, b.Kind, b.Project, b.Name, b.Version, b.Digest, jsonArg(b.Manifest),
		nullUUID(b.PublishedBy), b.PublishedByName).Scan(&b.PublishedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrVersionExists
	}
	return err
}

func nullUUID(u *uuid.UUID) any {
	if u == nil {
		return nil
	}
	return *u
}

// isUniqueViolation reads the two drivers' way of saying the same thing. Both
// spell it in the message; neither shares an error type with the other.
func isUniqueViolation(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "duplicate key") || strings.Contains(m, "unique constraint") || strings.Contains(m, "unique violation")
}

func (s *Store) Bundle(ctx context.Context, project, kind, name, version string) (*PublishedBundle, error) {
	return scanBundle(s.qrow(ctx, `SELECT `+bundleCols+` FROM published_bundles
		WHERE project=$1 AND kind=$2 AND name=$3 AND version=$4`, project, kind, name, version))
}

func (s *Store) BundleByDigest(ctx context.Context, digest string) (*PublishedBundle, error) {
	return scanBundle(s.qrow(ctx, `SELECT `+bundleCols+` FROM published_bundles WHERE digest=$1 LIMIT 1`, digest))
}

// BundleFilter narrows a listing. Project is how a scoped subject's listing is
// FILTERED rather than merely checked on fetch.
type BundleFilter struct {
	Project string
	Kind    string
	Name    string
}

func (s *Store) ListBundles(ctx context.Context, f BundleFilter) ([]*PublishedBundle, error) {
	q := `SELECT ` + bundleCols + ` FROM published_bundles`
	var where []string
	var args []any
	add := func(col, val string) {
		if val == "" {
			return
		}
		args = append(args, val)
		where = append(where, fmt.Sprintf("%s=$%d", col, len(args)))
	}
	add("project", f.Project)
	add("kind", f.Kind)
	add("name", f.Name)
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY published_at DESC"
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PublishedBundle{}
	for rows.Next() {
		b, err := scanBundle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---- tags ----

// SetBundleTag moves a tag. A tag is the only thing that moves: the version
// rows it points at and their content are untouched by this statement.
func (s *Store) SetBundleTag(ctx context.Context, project, kind, name, tag, version string) error {
	_, err := s.exec(ctx, `
		INSERT INTO bundle_tags (project, kind, name, tag, version) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (project, kind, name, tag) DO UPDATE SET version=EXCLUDED.version, updated_at=now()`,
		project, kind, name, tag, version)
	return err
}

func (s *Store) BundleTag(ctx context.Context, project, kind, name, tag string) (string, error) {
	var v string
	err := s.qrow(ctx, `SELECT version FROM bundle_tags WHERE project=$1 AND kind=$2 AND name=$3 AND tag=$4`,
		project, kind, name, tag).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}
