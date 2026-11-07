package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Sessions and scoped members (ADR 0028).

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

// UsersWithRole lists the users holding a users.role value.
func (s *Store) UsersWithRole(ctx context.Context, role string) ([]*User, error) {
	rows, err := s.query(ctx, `SELECT `+userCols+` FROM users WHERE role=$1 ORDER BY name`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserRole sets users.role ("superadmin" or "").
func (s *Store) SetUserRole(ctx context.Context, name, role string) error {
	_, err := s.exec(ctx, `UPDATE users SET role=$2 WHERE name=$1`, name, role)
	return err
}

// The scopes and the three scoped roles.
const (
	MemberProject  = "project"
	MemberWorkflow = "workflow"
	ProjectAdmin   = "project_admin"
	WorkflowAdmin  = "workflow_admin"
	WorkflowRunner = "workflow_runner"
)

// Member is one grant: a user holds a role on a project or a workflow.
type Member struct {
	Scope   string    `json:"scope"`
	Name    string    `json:"name"`
	User    string    `json:"user"`
	Role    string    `json:"role"`
	Created time.Time `json:"createdAt"`
}

// SetMember grants (or changes) a user's role at a scope. The table's CHECK
// refuses a role that does not belong to the scope.
func (s *Store) SetMember(ctx context.Context, scope, name string, userID uuid.UUID, role string) error {
	_, err := s.exec(ctx, `
		INSERT INTO members (scope, name, user_id, role) VALUES ($1,$2,$3,$4)
		ON CONFLICT (scope, name, user_id) DO UPDATE SET role = excluded.role`, scope, name, userID, role)
	return err
}

// RemoveMember drops a grant. ErrNotFound when there was none.
func (s *Store) RemoveMember(ctx context.Context, scope, name string, userID uuid.UUID) error {
	res, err := s.exec(ctx, `DELETE FROM members WHERE scope=$1 AND name=$2 AND user_id=$3`, scope, name, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) scanMembers(rows *sql.Rows, err error) ([]*Member, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Member{}
	for rows.Next() {
		m := &Member{}
		if err := rows.Scan(&m.Scope, &m.Name, &m.User, &m.Role, &m.Created); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMembers lists the grants at one scope.
func (s *Store) ListMembers(ctx context.Context, scope, name string) ([]*Member, error) {
	return s.scanMembers(s.query(ctx, `
		SELECT m.scope, m.name, u.name, m.role, m.created_at
		  FROM members m JOIN users u ON u.id = m.user_id
		 WHERE m.scope=$1 AND m.name=$2 ORDER BY u.name`, scope, name))
}

// GrantsOf lists every grant one user holds.
func (s *Store) GrantsOf(ctx context.Context, userID uuid.UUID) ([]*Member, error) {
	return s.scanMembers(s.query(ctx, `
		SELECT m.scope, m.name, u.name, m.role, m.created_at
		  FROM members m JOIN users u ON u.id = m.user_id
		 WHERE m.user_id=$1 ORDER BY m.scope, m.name`, userID))
}
