package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Issuer links (migration 000033): which local tenant an external issuer's
// tenant id maps to, and per-account revocations by sign-in time.

var ErrLinkExists = errors.New("that external tenant is linked to another tenant")

// LinkIssuerTenant maps (issuer, externalTenant) to a local tenant. Linking it
// again to the same tenant is a no-op; to a different one, ErrLinkExists.
func (s *Store) LinkIssuerTenant(ctx context.Context, tenant, issuer, externalTenant string) error {
	if issuer == "" || externalTenant == "" {
		return errors.New("issuer and external tenant are required")
	}
	if _, err := s.GetTenant(ctx, tenant); err != nil {
		return err
	}
	cur, err := s.IssuerTenant(ctx, issuer, externalTenant)
	switch {
	case err == nil && cur == tenant:
		return nil
	case err == nil:
		return ErrLinkExists
	case !errors.Is(err, ErrNotFound):
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO tenant_issuer_links (issuer, external_tenant, tenant_id) VALUES ($1,$2,$3)`, issuer, externalTenant, tenant)
	if err != nil && isUniqueViolation(err) {
		return ErrLinkExists
	}
	return err
}

// IssuerTenant names the local tenant an external tenant id maps to.
func (s *Store) IssuerTenant(ctx context.Context, issuer, externalTenant string) (string, error) {
	var t string
	err := s.qrow(ctx, `SELECT tenant_id FROM tenant_issuer_links WHERE issuer=$1 AND external_tenant=$2`, issuer, externalTenant).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
}

// RevokeIssuerSubject refuses every token of that account whose sign-in
// (auth_time) is before `before`.
func (s *Store) RevokeIssuerSubject(ctx context.Context, issuer, externalTenant, subject string, before time.Time) error {
	if subject == "" {
		return errors.New("subject is required")
	}
	_, err := s.exec(ctx, `DELETE FROM issuer_revocations WHERE issuer=$1 AND external_tenant=$2 AND subject=$3`, issuer, externalTenant, subject)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO issuer_revocations (issuer, external_tenant, subject, revoked_before) VALUES ($1,$2,$3,$4)`,
		issuer, externalTenant, subject, before.UTC())
	return err
}

// IssuerRevokedBefore returns the revocation time for an account, if any.
func (s *Store) IssuerRevokedBefore(ctx context.Context, issuer, externalTenant, subject string) (time.Time, bool, error) {
	var t time.Time
	err := s.qrow(ctx, `SELECT revoked_before FROM issuer_revocations WHERE issuer=$1 AND external_tenant=$2 AND subject=$3`, issuer, externalTenant, subject).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return t, err == nil, err
}
