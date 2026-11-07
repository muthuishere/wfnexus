package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Sessions and per-workflow members (ADR 0028).

// UserBySession resolves an access token's sid: the refresh row must still
// exist (not revoked) and its user must be enabled. This is what makes a
// revoke take effect on the next request rather than at JWT expiry.
func (s *Store) UserBySession(ctx context.Context, sid uuid.UUID) (*User, *UserToken, error) {
	u := &User{}
	t := &UserToken{}
	err := s.qrow(ctx, `
		SELECT u.id, u.name, u.display_name, u.role, u.project, u.disabled, u.must_change, u.password_hash <> '', u.created_at,
		       t.id, t.label, t.project, t.created_at
		  FROM user_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.id=$1 AND u.disabled = false`, sid).
		Scan(&u.ID, &u.Name, &u.DisplayName, &u.Role, &u.Project, &u.Disabled, &u.MustChange, &u.HasPassword, &u.Created,
			&t.ID, &t.Label, &t.Project, &t.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	t.UserID = u.ID
	return u, t, nil
}

// RevokeUserToken deletes one token row by id: a session's logout.
func (s *Store) RevokeUserToken(ctx context.Context, id uuid.UUID) error {
	res, err := s.exec(ctx, `DELETE FROM user_tokens WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// WorkflowMember is one per-workflow grant: owner or editor.
type WorkflowMember struct {
	Workflow string    `json:"workflow"`
	User     string    `json:"user"`
	Role     string    `json:"role"`
	Created  time.Time `json:"createdAt"`
}

// SetWorkflowMember grants (or changes) a user's role on one workflow.
func (s *Store) SetWorkflowMember(ctx context.Context, workflow string, userID uuid.UUID, role string) error {
	_, err := s.exec(ctx, `
		INSERT INTO workflow_members (workflow, user_id, role) VALUES ($1,$2,$3)
		ON CONFLICT (workflow, user_id) DO UPDATE SET role = excluded.role`, workflow, userID, role)
	return err
}

// RemoveWorkflowMember drops a grant. ErrNotFound when there was none.
func (s *Store) RemoveWorkflowMember(ctx context.Context, workflow string, userID uuid.UUID) error {
	res, err := s.exec(ctx, `DELETE FROM workflow_members WHERE workflow=$1 AND user_id=$2`, workflow, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// WorkflowMemberRole is the user's role on a workflow, "" when none.
func (s *Store) WorkflowMemberRole(ctx context.Context, workflow string, userID uuid.UUID) (string, error) {
	var role string
	err := s.qrow(ctx, `SELECT role FROM workflow_members WHERE workflow=$1 AND user_id=$2`, workflow, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// ListWorkflowMembers lists a workflow's grants by user name.
func (s *Store) ListWorkflowMembers(ctx context.Context, workflow string) ([]*WorkflowMember, error) {
	rows, err := s.query(ctx, `
		SELECT m.workflow, u.name, m.role, m.created_at
		  FROM workflow_members m JOIN users u ON u.id = m.user_id
		 WHERE m.workflow=$1 ORDER BY u.name`, workflow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WorkflowMember{}
	for rows.Next() {
		m := &WorkflowMember{}
		if err := rows.Scan(&m.Workflow, &m.User, &m.Role, &m.Created); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
