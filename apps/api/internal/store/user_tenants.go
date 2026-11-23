package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// User tenants (migration 000036, ADR 0035): the one organization a person
// signed in through an OIDC provider with orgClaim belongs to, and their role
// in it. A user with no row is unscoped, which is every user on an install
// whose providers set no orgClaim.

// ErrUserTenantBound: the user is already bound to a different tenant. A
// person never moves between organizations by signing in.
var ErrUserTenantBound = errors.New("user belongs to another tenant")

// UserTenant names the tenant a user is bound to and their role in it. A user
// with no binding is ("", "", nil): unscoped, not an error. Any other error is
// returned as is, so a caller can fail closed rather than read it as
// "unscoped".
func (s *Store) UserTenant(ctx context.Context, userID uuid.UUID) (tenant, role string, err error) {
	err = s.qrow(ctx, `SELECT tenant_id, role FROM user_tenants WHERE user_id=$1`, userID).Scan(&tenant, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return tenant, role, err
}

// BindUserTenant binds a user to a tenant with a role, or refreshes the role
// of an existing binding to the SAME tenant. A binding to a different tenant
// is left untouched and answered with ErrUserTenantBound.
//
// It is one conditional upsert, so two sign-ins racing with different
// organizations cannot both bind: the second finds the row and, its tenant
// not matching, changes nothing.
func (s *Store) BindUserTenant(ctx context.Context, userID uuid.UUID, tenant, role string) error {
	res, err := s.exec(ctx, `INSERT INTO user_tenants (user_id, tenant_id, role) VALUES ($1,$2,$3)
		ON CONFLICT (user_id) DO UPDATE SET role=excluded.role
		WHERE user_tenants.tenant_id = excluded.tenant_id`, userID, tenant, role)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrUserTenantBound
	}
	return nil
}

// UserProvider names the login provider a user is bound to ("" for a local
// account). The organization policy is read from that provider's config.
func (s *Store) UserProvider(ctx context.Context, userID uuid.UUID) (string, error) {
	var p string
	err := s.qrow(ctx, `SELECT auth_provider FROM users WHERE id=$1`, userID).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return p, err
}
