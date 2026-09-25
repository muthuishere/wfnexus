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

// The published-bundle INDEX — and an index is all it is.
//
// ADR 0018 made this the registry. The change "a git remote is the registry"
// (design §4) demoted it: GIT is the registry, and this table is a LOCAL INDEX
// of what this host has seen. The two views answer different questions and are
// not ranked — git answers *where does this live and who approved it*, this
// table answers *what is present here right now*, which is the question a host
// serving a run from its own cache has to answer with no network.
//
// So nothing here is the source of truth about a published bundle:
//   - GitRemote + GitCommit say which git row this one mirrors, when it came
//     from git at all. Absent for a direct upload — nil, not "".
//   - PublishedBy is a cached copy of a publisher, not the provenance record.
//     See its own comment.
//
// The BYTES are not here either: they are in the blob store, which is itself a
// cache (design §3). This table holds only what has to be queried.

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
	// GitRemote and GitCommit are where this bundle was READ FROM, when it came
	// from a git remote — the remote as git was given it, and the RESOLVED
	// COMMIT, never the tag, because a tag moves and a commit cannot (design
	// §3). Together they are what lets this index row and the git history be
	// reconciled: same digest, same commit, one bundle.
	//
	// nil for a bundle uploaded to this host directly. That bundle has no
	// remote and no commit, and "" is not a remote — absent, not empty.
	GitRemote *string `json:"gitRemote,omitempty"`
	GitCommit *string `json:"gitCommit,omitempty"`
	// PublishedBy / PublishedByName: WHO THIS HOST WAS TOLD PUBLISHED IT —
	// DEMOTED, per design §5, and kept.
	//
	// It is still filled from the AUTHENTICATED SUBJECT and never from a request
	// body, and PublishedByName is still denormalised so it survives the user
	// row being deleted. What changed is its standing: for a bundle that came
	// from git, the COMMIT AUTHOR is the record of who published — a commit has
	// an author, a committer, a date, a message and a parent, and a signed tag
	// is tamper-evidence this column never was. So read it as a cached local
	// attribution, and read GitCommit when you need provenance. For a direct
	// upload it remains the only answer there is, which is why the column is
	// kept rather than dropped.
	PublishedBy     *uuid.UUID `json:"publishedBy,omitempty"`
	PublishedByName string     `json:"publishedBy_name"`
	PublishedAt     time.Time  `json:"publishedAt"`
}

// FromGit reports whether this row mirrors a bundle read from a git remote.
// Both fields or neither: CreateBundle refuses a half-record.
func (b *PublishedBundle) FromGit() bool { return b.GitRemote != nil && b.GitCommit != nil }

// ErrGitOriginHalf is the both-or-neither refusal. A remote with no commit
// names a place without naming what was read from it, so the two views can
// never be joined — the whole reason the columns exist.
var ErrGitOriginHalf = errors.New("a git origin is a remote AND a resolved commit, or neither")

const bundleCols = `id, kind, project, name, version, digest, manifest, git_remote, git_commit, published_by, published_by_name, published_at`

func scanBundle(row rowScanner) (*PublishedBundle, error) {
	b := &PublishedBundle{}
	var by uuid.NullUUID
	var remote, commit sql.NullString
	err := row.Scan(&b.ID, &b.Kind, &b.Project, &b.Name, &b.Version, &b.Digest,
		rawJSON{&b.Manifest}, &remote, &commit, &by, &b.PublishedByName, &b.PublishedAt)
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
	// NULL stays nil. A row from before the git sink has neither, and so does a
	// direct upload; neither becomes an empty string on the way out.
	if remote.Valid {
		v := remote.String
		b.GitRemote = &v
	}
	if commit.Valid {
		v := commit.String
		b.GitCommit = &v
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
	// Both or neither, checked here because SQLite cannot add a table CHECK to
	// an existing table and both dialects must accept the same rows. Postgres
	// states it as a constraint as well (migration 000013), so the rule holds
	// even for a writer that is not this function.
	if (b.GitRemote == nil) != (b.GitCommit == nil) {
		return ErrGitOriginHalf
	}
	err := s.qrow(ctx, `
		INSERT INTO published_bundles (id, kind, project, name, version, digest, manifest, git_remote, git_commit, published_by, published_by_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11) RETURNING published_at`,
		b.ID, b.Kind, b.Project, b.Name, b.Version, b.Digest, jsonArg(b.Manifest),
		nullString(b.GitRemote), nullString(b.GitCommit),
		nullUUID(b.PublishedBy), b.PublishedByName).Scan(&b.PublishedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrVersionExists
	}
	return err
}

// nullString keeps ABSENT absent: a nil pointer becomes SQL NULL, never "".
func nullString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
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
