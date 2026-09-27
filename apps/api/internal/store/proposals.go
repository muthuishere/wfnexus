package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Proposal is one pending (or decided) workflow change. Git holds the change
// itself — the branch, the commit, the PR; this row holds what git does not:
// who asked, what the classifier advised, and who decided. See
// internal/proposal for the git half.
type Proposal struct {
	ID       uuid.UUID `json:"id"`
	Project  string    `json:"project"`
	Workflow string    `json:"workflow"`
	Kind     string    `json:"kind"` // create|edit|delete
	Branch   string    `json:"branch"`
	Base     string    `json:"base"`
	Commit   string    `json:"commit"`
	PRURL    string    `json:"prUrl"`
	Status   string    `json:"status"` // pending|approved|rejected|merged
	// The classifier's advice; the human's approve/reject decides.
	ReviewVerdict string   `json:"reviewVerdict"` // approve|reject|unreviewed
	ReviewReason  string   `json:"reviewReason"`
	ReviewScore   *float64 `json:"reviewScore,omitempty"`
	// Note records a non-fatal failure while opening (no push, no PR) or while
	// merging, so a proposal says why it is not where one would expect.
	Note      string     `json:"note,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	CreatedBy string     `json:"createdBy"`
	DecidedBy string     `json:"decidedBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
}

const proposalCols = `id, project, workflow, kind, branch, base_branch, commit_sha, pr_url, status,
	review_verdict, review_reason, review_score, note, reason, created_by, decided_by, created_at, decided_at`

func scanProposal(row rowScanner) (*Proposal, error) {
	p := &Proposal{}
	var score sql.NullFloat64
	err := row.Scan(&p.ID, &p.Project, &p.Workflow, &p.Kind, &p.Branch, &p.Base, &p.Commit, &p.PRURL, &p.Status,
		&p.ReviewVerdict, &p.ReviewReason, &score, &p.Note, &p.Reason, &p.CreatedBy, &p.DecidedBy,
		&p.CreatedAt, &p.DecidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if score.Valid {
		v := score.Float64
		p.ReviewScore = &v
	}
	return p, nil
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// CreateProposal records a proposal that git has already opened.
func (s *Store) CreateProposal(ctx context.Context, p *Proposal) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	if p.Status == "" {
		p.Status = "pending"
	}
	if p.ReviewVerdict == "" {
		p.ReviewVerdict = "unreviewed"
	}
	return s.qrow(ctx, `
		INSERT INTO workflow_proposals (id, project, workflow, kind, branch, base_branch, commit_sha, pr_url, status,
			review_verdict, review_reason, review_score, note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING created_at`,
		p.ID, p.Project, p.Workflow, p.Kind, p.Branch, p.Base, p.Commit, p.PRURL, p.Status,
		p.ReviewVerdict, p.ReviewReason, nullFloat(p.ReviewScore), p.Note, p.CreatedBy).Scan(&p.CreatedAt)
}

func (s *Store) GetProposal(ctx context.Context, id uuid.UUID) (*Proposal, error) {
	return scanProposal(s.qrow(ctx, `SELECT `+proposalCols+` FROM workflow_proposals WHERE id=$1`, id))
}

// ProposalFilter narrows a listing; every field is optional.
type ProposalFilter struct {
	Project  string
	Workflow string
	Status   string
}

func (s *Store) ListProposals(ctx context.Context, f ProposalFilter) ([]*Proposal, error) {
	q := `SELECT ` + proposalCols + ` FROM workflow_proposals`
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
	add("workflow", f.Workflow)
	add("status", f.Status)
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ErrProposalDecided refuses a second decision on the same proposal.
var ErrProposalDecided = errors.New("proposal already decided")

// DecideProposal moves a PENDING (or approved-but-unmerged) proposal to a
// final status. The WHERE clause is the guard: two people deciding at once
// get one decision and one refusal, not two.
func (s *Store) DecideProposal(ctx context.Context, id uuid.UUID, status, by, reason, note string) error {
	res, err := s.exec(ctx, `
		UPDATE workflow_proposals SET status=$2, decided_by=$3, reason=$4, note=$5, decided_at=now()
		WHERE id=$1 AND status IN ('pending','approved')`, id, status, by, reason, note)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetProposal(ctx, id); err != nil {
			return err
		}
		return ErrProposalDecided
	}
	return nil
}

// SetProposalReview stores the classifier's advice after the fact.
func (s *Store) SetProposalReview(ctx context.Context, id uuid.UUID, verdict, reason string, score *float64) error {
	_, err := s.exec(ctx, `UPDATE workflow_proposals SET review_verdict=$2, review_reason=$3, review_score=$4 WHERE id=$1`,
		id, verdict, reason, nullFloat(score))
	return err
}
