package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Identity: users, roles, user tokens and the RFC 8628 device-grant rows.
//
// A user token is looked up by HASH and only by hash — there is no query here
// that takes a token value, because a query that did would put the value in a
// statement, and statements get logged. Callers hash first (internal/auth) and
// pass the hash down.

// ErrNotFound is what every identity lookup returns when nothing matches, so a
// caller cannot accidentally distinguish "revoked" from "never existed".
var ErrNotFound = errors.New("not found")

type User struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	Role        string    `json:"role"`
	Project     string    `json:"project"`
	Disabled    bool      `json:"disabled"`
	// MustChange: the next password sign-in must set a new password (ADR 0025).
	MustChange bool `json:"mustChange"`
	// HasPassword says whether a password is set. The hash itself is never
	// on this struct, so it can never be marshalled.
	HasPassword bool      `json:"hasPassword"`
	Created     time.Time `json:"createdAt"`
}

// UserToken is the METADATA of a credential. There is deliberately no field
// for the value: the value exists for the length of one response and is then
// only a hash (identity spec, "A token value is shown once").
type UserToken struct {
	ID       uuid.UUID  `json:"id"`
	UserID   uuid.UUID  `json:"userId"`
	Label    string     `json:"label"`
	Project  string     `json:"project"`
	Created  time.Time  `json:"createdAt"`
	LastUsed *time.Time `json:"lastUsed,omitempty"`
}

const userCols = `id, name, display_name, role, project, disabled, must_change, password_hash <> '', created_at`

func scanUser(row rowScanner) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Role, &u.Project, &u.Disabled, &u.MustChange, &u.HasPassword, &u.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	return s.qrow(ctx, `
		INSERT INTO users (id, name, display_name, role, project)
		VALUES ($1,$2,$3,$4,$5) RETURNING `+userCols,
		u.ID, u.Name, u.DisplayName, u.Role, u.Project,
	).Scan(&u.ID, &u.Name, &u.DisplayName, &u.Role, &u.Project, &u.Disabled, &u.MustChange, &u.HasPassword, &u.Created)
}

func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.qrow(ctx, `SELECT `+userCols+` FROM users WHERE name=$1`, name))
}

// ---- external sign-in: users keyed by (provider, subject) ----

// Link states of a user (users.link_state).
const (
	LinkLegacy  = "legacy"  // made by an external sign-in before subjects were kept: bound once, on the next sign-in
	LinkLinked  = "linked"  // bound that way, held no roles: signs in, open to review (confirm or reject)
	LinkConfirm = "confirm" // bound, but it held roles: signs in only after an admin confirms
)

// UserBySubject is the user an external provider's subject is bound to;
// ErrNotFound when none is.
func (s *Store) UserBySubject(ctx context.Context, provider, subject string) (*User, error) {
	return scanUser(s.qrow(ctx, `SELECT `+userCols+` FROM users WHERE auth_provider=$1 AND auth_subject=$2 AND auth_subject <> ''`, provider, subject))
}

// LinkState is a user's link state ("" when nothing is pending).
func (s *Store) LinkState(ctx context.Context, id uuid.UUID) (string, error) {
	var st string
	err := s.qrow(ctx, `SELECT link_state FROM users WHERE id=$1`, id).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return st, err
}

// SetSubject records the subject on a user that has none (a user just made
// by an external sign-in). A user that already has a subject is never
// re-bound. It reports whether it bound.
func (s *Store) SetSubject(ctx context.Context, id uuid.UUID, provider, subject string) (bool, error) {
	return s.bind(ctx, `UPDATE users SET auth_provider=$2, auth_subject=$3
		WHERE id=$1 AND auth_subject=''`, id, provider, subject)
}

// BindLegacy binds a legacy row to the subject that signed in with its name —
// once: only while the row is still legacy, and never to a subject an admin
// rejected for it — leaving it in state ("", LinkLinked or LinkConfirm) with
// the name the person claimed. It reports whether it bound.
func (s *Store) BindLegacy(ctx context.Context, id uuid.UUID, provider, subject, claim, state string) (bool, error) {
	return s.bind(ctx, `UPDATE users SET auth_provider=$2, auth_subject=$3, auth_claim=$4, link_state=$5
		WHERE id=$1 AND auth_subject='' AND link_state='`+LinkLegacy+`' AND link_rejected<>$3`, id, provider, subject, claim, state)
}

// ReKeySubject moves a user's binding from an old subject to a new one for
// the same person (an LDAP binding re-keyed from the username to the entry's
// stable id). It reports whether it moved.
func (s *Store) ReKeySubject(ctx context.Context, provider, from, to string) (bool, error) {
	return s.bind(ctx, `UPDATE users SET auth_subject=$3 WHERE auth_provider=$1 AND auth_subject=$2 AND $2<>''`, provider, from, to)
}

// RejectLink undoes a binding made on first sign-in after the upgrade: the
// account is unbound, back to legacy, its sessions and tokens end, and the
// rejected subject may not bind it again. ErrNotFound when it has no such
// binding.
func (s *Store) RejectLink(ctx context.Context, name string) error {
	var id uuid.UUID
	err := s.qrow(ctx, `UPDATE users SET link_rejected=auth_subject, auth_provider='', auth_subject='', auth_claim='', link_state=$2
		WHERE name=$1 AND link_state IN ($3, $4) RETURNING id`, name, LinkLegacy, LinkLinked, LinkConfirm).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.RevokeAllUserTokens(ctx, id)
}

func (s *Store) bind(ctx context.Context, q string, args ...any) (bool, error) {
	tag, err := s.exec(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := tag.RowsAffected()
	return n == 1, nil
}

// RevokeAllUserTokens ends every session, refresh token and API token of a
// user (an account whose link waits for confirmation keeps none).
func (s *Store) RevokeAllUserTokens(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx, `DELETE FROM user_tokens WHERE user_id=$1`, id)
	return err
}

// HasGrants says whether a user holds any role: superadmin, or a project or
// workflow membership.
func (s *Store) HasGrants(ctx context.Context, id uuid.UUID) (bool, error) {
	var n int
	err := s.qrow(ctx, `SELECT (CASE WHEN role <> '' THEN 1 ELSE 0 END) +
		(SELECT count(*) FROM members WHERE user_id=$1) FROM users WHERE id=$1`, id).Scan(&n)
	return n > 0, err
}

// PendingLink is an account bound on its first sign-in after the upgrade,
// open to review: State is LinkConfirm (held roles; signs in only once
// confirmed) or LinkLinked (held none; signs in). Claim is the username or
// email the person signed in with.
type PendingLink struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
	Claim    string `json:"claim"`
	State    string `json:"state"`
}

func (s *Store) PendingLinks(ctx context.Context) ([]PendingLink, error) {
	rows, err := s.query(ctx, `SELECT name, auth_provider, auth_subject, auth_claim, link_state FROM users
		WHERE link_state IN ($1, $2) ORDER BY name`, LinkConfirm, LinkLinked)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingLink{}
	for rows.Next() {
		var p PendingLink
		if err := rows.Scan(&p.Name, &p.Provider, &p.Subject, &p.Claim, &p.State); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ConfirmLink confirms a link under review; ErrNotFound when the user has
// none.
func (s *Store) ConfirmLink(ctx context.Context, name string) error {
	tag, err := s.exec(ctx, `UPDATE users SET link_state='' WHERE name=$1 AND link_state IN ($2, $3)`, name, LinkConfirm, LinkLinked)
	if err != nil {
		return err
	}
	if n, _ := tag.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.query(ctx, `SELECT `+userCols+` FROM users ORDER BY name`)
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

// PasswordHashByName returns an ENABLED user and their stored password hash
// (empty when none is set). A disabled user is ErrNotFound, exactly like an
// unknown one, so the sign-in handler cannot tell them apart.
func (s *Store) PasswordHashByName(ctx context.Context, name string) (*User, string, error) {
	var hash string
	u := &User{}
	err := s.qrow(ctx, `SELECT `+userCols+`, password_hash FROM users WHERE name=$1 AND disabled = false`, name).
		Scan(&u.ID, &u.Name, &u.DisplayName, &u.Role, &u.Project, &u.Disabled, &u.MustChange, &u.HasPassword, &u.Created, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return u, hash, nil
}

// SetPassword stores a new hash (computed by the caller) and whether the next
// sign-in must replace it.
func (s *Store) SetPassword(ctx context.Context, name, hash string, mustChange bool) error {
	res, err := s.exec(ctx, `UPDATE users SET password_hash=$2, must_change=$3 WHERE name=$1`, name, hash, mustChange)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountUsers is what the boot refusal reads: a non-loopback bind with zero
// users must not serve (design §4.6).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.qrow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ---- user tokens ----

// IssueUserToken stores ONLY the hash the caller computed. The value never
// reaches this package.
func (s *Store) IssueUserToken(ctx context.Context, userID uuid.UUID, tokenHash, label, project string) (*UserToken, error) {
	t := &UserToken{ID: uuid.New(), UserID: userID, Label: label, Project: project}
	err := s.qrow(ctx, `
		INSERT INTO user_tokens (id, user_id, token_hash, label, project)
		VALUES ($1,$2,$3,$4,$5) RETURNING created_at`,
		t.ID, userID, tokenHash, label, project).Scan(&t.Created)
	return t, err
}

// UserByTokenHash resolves a bearer to its owner and touches last_used. A
// disabled user resolves to nothing — the row survives for the audit trail but
// the credential stops working.
func (s *Store) UserByTokenHash(ctx context.Context, tokenHash string) (*User, *UserToken, error) {
	u := &User{}
	t := &UserToken{}
	err := s.qrow(ctx, `
		SELECT u.id, u.name, u.display_name, u.role, u.project, u.disabled, u.must_change, u.password_hash <> '', u.created_at,
		       t.id, t.label, t.project, t.created_at
		  FROM user_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash=$1 AND u.disabled = false`, tokenHash).
		Scan(&u.ID, &u.Name, &u.DisplayName, &u.Role, &u.Project, &u.Disabled, &u.MustChange, &u.HasPassword, &u.Created,
			&t.ID, &t.Label, &t.Project, &t.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	t.UserID = u.ID
	// Best effort: a failed touch must not refuse an otherwise valid request.
	_, _ = s.exec(ctx, `UPDATE user_tokens SET last_used=now() WHERE id=$1`, t.ID)
	return u, t, nil
}

// ListUserTokens returns metadata only — see UserToken.
func (s *Store) ListUserTokens(ctx context.Context, userID uuid.UUID) ([]*UserToken, error) {
	rows, err := s.query(ctx, `
		SELECT id, user_id, label, project, created_at, last_used
		  FROM user_tokens WHERE user_id=$1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*UserToken{}
	for rows.Next() {
		t := &UserToken{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.Label, &t.Project, &t.Created, &t.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeUserTokenByHash is what `wfx logout` reaches: the client knows its own
// value, so it can revoke exactly its own row and nobody else's.
func (s *Store) RevokeUserTokenByHash(ctx context.Context, tokenHash string) error {
	res, err := s.exec(ctx, `DELETE FROM user_tokens WHERE token_hash=$1`, tokenHash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- roles ----

func (s *Store) UpsertRole(ctx context.Context, name string, perms []string) error {
	_, err := s.exec(ctx, `
		INSERT INTO roles (name, permissions) VALUES ($1,$2)
		ON CONFLICT (name) DO UPDATE SET permissions=EXCLUDED.permissions`,
		name, s.d.labelsArg(perms))
	return err
}

func (s *Store) Role(ctx context.Context, name string) ([]string, error) {
	var perms []string
	err := s.qrow(ctx, `SELECT permissions FROM roles WHERE name=$1`, name).Scan(labelsScan{s.d, &perms})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return perms, err
}

func (s *Store) CountRoles(ctx context.Context) (int, error) {
	var n int
	err := s.qrow(ctx, `SELECT count(*) FROM roles`).Scan(&n)
	return n, err
}

// ---- device codes (RFC 8628) ----

type DeviceCode struct {
	UserCode   string
	ClientID   string
	HostName   string
	Scope      string
	Status     string
	ApprovedBy *uuid.UUID
	Attempts   int
	ExpiresAt  time.Time
}

// CreateDeviceCode stores the device code HASHED — for the length of the flow
// it is a bearer credential, so it obeys the same storage rule as a token.
func (s *Store) CreateDeviceCode(ctx context.Context, deviceHash, userCode, clientID, hostName, scope string, expires time.Time) error {
	_, err := s.exec(ctx, `
		INSERT INTO device_codes (device_code, user_code, client_id, host_name, scope, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, deviceHash, userCode, clientID, hostName, scope, expires)
	return err
}

func (s *Store) DeviceCodeByHash(ctx context.Context, deviceHash string) (*DeviceCode, error) {
	return s.scanDevice(s.qrow(ctx, `
		SELECT user_code, client_id, host_name, scope, status, approved_by, attempts, expires_at
		  FROM device_codes WHERE device_code=$1`, deviceHash))
}

func (s *Store) DeviceCodeByUserCode(ctx context.Context, userCode string) (*DeviceCode, error) {
	return s.scanDevice(s.qrow(ctx, `
		SELECT user_code, client_id, host_name, scope, status, approved_by, attempts, expires_at
		  FROM device_codes WHERE user_code=$1`, userCode))
}

func (s *Store) scanDevice(row rowScanner) (*DeviceCode, error) {
	d := &DeviceCode{}
	var approved uuid.NullUUID
	err := row.Scan(&d.UserCode, &d.ClientID, &d.HostName, &d.Scope, &d.Status, &approved, &d.Attempts, &d.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if approved.Valid {
		id := approved.UUID
		d.ApprovedBy = &id
	}
	return d, nil
}

// ApproveDeviceCode binds the approving user to the code. It matches only a
// PENDING, unexpired row, so an approval cannot resurrect a denied or burned
// code.
func (s *Store) ApproveDeviceCode(ctx context.Context, userCode string, by uuid.UUID) error {
	// The expiry is compared against a BOUND time rather than the engine's
	// now(): the two engines spell and store timestamps differently, and a
	// parameter is the one form both compare the same way.
	res, err := s.exec(ctx, `
		UPDATE device_codes SET status='approved', approved_by=$2
		 WHERE user_code=$1 AND status='pending' AND expires_at > $3`, userCode, by, time.Now().UTC())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DenyDeviceCode(ctx context.Context, userCode string) error {
	_, err := s.exec(ctx, `UPDATE device_codes SET status='denied' WHERE user_code=$1`, userCode)
	return err
}

// BumpDeviceAttempts counts a failed user-code entry (RFC 8628 §5.2) and
// returns the new count, so the caller can burn the code at the limit.
func (s *Store) BumpDeviceAttempts(ctx context.Context, userCode string) (int, error) {
	if _, err := s.exec(ctx, `UPDATE device_codes SET attempts=attempts+1 WHERE user_code=$1`, userCode); err != nil {
		return 0, err
	}
	var n int
	err := s.qrow(ctx, `SELECT attempts FROM device_codes WHERE user_code=$1`, userCode).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return n, err
}

// TouchDevicePoll records a poll and reports whether it arrived sooner than the
// interval the server handed out (RFC 8628 §3.5). The previous poll time is
// read and rewritten in one statement's worth of work, so two racing polls
// cannot both be judged patient.
func (s *Store) TouchDevicePoll(ctx context.Context, userCode string, intervalSec int) (bool, error) {
	var last *time.Time
	err := s.qrow(ctx, `SELECT last_poll FROM device_codes WHERE user_code=$1`, userCode).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if _, err := s.exec(ctx, `UPDATE device_codes SET last_poll=$2 WHERE user_code=$1`, userCode, now); err != nil {
		return false, err
	}
	if last == nil {
		return false, nil
	}
	return now.Sub(last.UTC()) < time.Duration(intervalSec)*time.Second, nil
}

// DeleteDeviceCodeByUserCode is how a code is BURNED — on success, denial,
// expiry or too many failed entries. RFC 8628 §3.5: a device code MUST NOT be
// reused, and the cheapest way to guarantee that is for the row to be gone.
func (s *Store) DeleteDeviceCodeByUserCode(ctx context.Context, userCode string) error {
	_, err := s.exec(ctx, `DELETE FROM device_codes WHERE user_code=$1`, userCode)
	return err
}

// ClaimDeviceCode deletes a code and reports whether THIS call was the one
// that deleted it. Two racing redemptions of the same single-use code cannot
// both see true, which a read-then-delete could not promise.
func (s *Store) ClaimDeviceCode(ctx context.Context, userCode string) (bool, error) {
	res, err := s.exec(ctx, `DELETE FROM device_codes WHERE user_code=$1`, userCode)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// PurgeExpiredDeviceCodes keeps the table from becoming a graveyard of codes
// nobody ever typed.
func (s *Store) PurgeExpiredDeviceCodes(ctx context.Context) error {
	_, err := s.exec(ctx, `DELETE FROM device_codes WHERE expires_at <= $1`, time.Now().UTC())
	return err
}

// ---- admin CRUD ----

// UpdateUser changes what an administrator may change: the display name, the
// role, the project scope and whether the credential still works. The name is
// the identity and is not one of them.
func (s *Store) UpdateUser(ctx context.Context, name, displayName, role, project string, disabled bool) (*User, error) {
	return scanUser(s.qrow(ctx, `
		UPDATE users SET display_name=$2, role=$3, project=$4, disabled=$5
		 WHERE name=$1 RETURNING `+userCols, name, displayName, role, project, disabled))
}

func (s *Store) DeleteUser(ctx context.Context, name string) error {
	res, err := s.exec(ctx, `DELETE FROM users WHERE name=$1`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountAdminsExcept is what the last-administrator rule reads: how many admins
// would remain if this one changed. Asked as a question about the REST of the
// table, so the caller cannot get the arithmetic wrong.
func (s *Store) CountAdminsExcept(ctx context.Context, name string) (int, error) {
	var n int
	err := s.qrow(ctx, `SELECT count(*) FROM users WHERE role='superadmin' AND disabled=false AND name<>$1`, name).Scan(&n)
	return n, err
}

type Role struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

func (s *Store) ListRoles(ctx context.Context) ([]*Role, error) {
	rows, err := s.query(ctx, `SELECT name, permissions FROM roles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Role{}
	for rows.Next() {
		r := &Role{}
		if err := rows.Scan(&r.Name, labelsScan{s.d, &r.Permissions}); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountUsersWithRole is what refuses to delete a role somebody still holds:
// deleting it would leave a subject naming a role that does not exist, and
// every one of their requests would then be refused for a reason nobody
// intended.
func (s *Store) CountUsersWithRole(ctx context.Context, role string) (int, error) {
	var n int
	err := s.qrow(ctx, `SELECT count(*) FROM users WHERE role=$1`, role).Scan(&n)
	return n, err
}

func (s *Store) DeleteRole(ctx context.Context, name string) error {
	res, err := s.exec(ctx, `DELETE FROM roles WHERE name=$1`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
