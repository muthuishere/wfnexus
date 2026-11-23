package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"

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

// OrgTenantID is the tenant id an organization claim maps to on a provider:
// "org-" plus 32 hex characters of sha256(provider NUL claim). It is ALWAYS
// derived, never the claim itself, so that
//   - no claim can name an existing tenant (default, or one an operator made
//     for something else) and join it;
//   - the same claim on two providers is two tenants: one identity provider
//     cannot speak for another's organizations;
//   - no literal claim can equal another claim's id.
//
// An operator who wants a tenant to exist before anyone signs in (with
// autoCreateOrg off) creates it under this id. The readable claim is kept
// only as the tenant's name.
func OrgTenantID(provider, claim string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + claim))
	return "org-" + hex.EncodeToString(sum[:16])
}

// OrgTenantName is the name a tenant made from an organization claim is
// stored under: the claim with control and other unprintable characters
// dropped, so a claim can never write terminal escapes into a listing or a
// log. It is a label only; nothing looks a tenant up by it.
func OrgTenantName(claim string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, claim)
}

// BindUserTenantCreating is BindUserTenant that also creates the tenant
// when it does not exist yet, in ONE transaction: if the binding is refused
// (the user is bound elsewhere, by a sign-in racing this one) or fails, the
// tenant is not created either, so a refused sign-in leaves no orphan
// tenant. created says whether this call made the tenant.
func (s *Store) BindUserTenantCreating(ctx context.Context, userID uuid.UUID, tenant, name, role string) (created bool, err error) {
	if !ValidTenantID(tenant) {
		return false, ErrBadTenantID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, s.d.q(`INSERT INTO tenants (id, name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`), tenant, name)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else {
		created = n == 1
	}
	res, err = tx.ExecContext(ctx, s.d.q(`INSERT INTO user_tenants (user_id, tenant_id, role) VALUES ($1,$2,$3)
		ON CONFLICT (user_id) DO UPDATE SET role=excluded.role
		WHERE user_tenants.tenant_id = excluded.tenant_id`), userID, tenant, role)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else if n == 0 {
		return false, ErrUserTenantBound
	}
	return created, tx.Commit()
}

// UnbindUserTenant takes a person out of their organization: it deletes the
// binding AND every token they hold, in one transaction. Both, because a
// session minted while bound would otherwise go on working; their next
// sign-in binds them afresh from the claim.
func (s *Store) UnbindUserTenant(ctx context.Context, userID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.d.q(`DELETE FROM user_tenants WHERE user_id=$1`), userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.d.q(`DELETE FROM user_tokens WHERE user_id=$1`), userID); err != nil {
		return err
	}
	return tx.Commit()
}
