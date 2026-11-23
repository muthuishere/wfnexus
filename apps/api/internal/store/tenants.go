package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Tenants (migration 000032): a boundary that runs, published bundles and
// service principals belong to. An install with one owner has one tenant,
// DefaultTenant, and every row written before tenants existed belongs to it.
//
// Isolation is enforced here and in the API, not by the database: every
// tenant-scoped read in this file takes the tenant as an argument and filters
// on it in SQL.

// DefaultTenant owns every project that is not bound to another tenant.
const DefaultTenant = "default"

var (
	// ErrProjectBound: the project already belongs to a different tenant.
	// Moving a project between tenants is not supported.
	ErrProjectBound = errors.New("project belongs to another tenant")
	ErrTenantExists = errors.New("tenant exists")
	ErrBadTenantID  = errors.New("tenant id must be 1-63 of [a-z0-9-], starting with a letter or digit")
)

var tenantIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidTenantID reports whether id is a well-formed tenant id.
func ValidTenantID(id string) bool { return tenantIDRe.MatchString(id) }

type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) CreateTenant(ctx context.Context, id, name string) (*Tenant, error) {
	if !ValidTenantID(id) {
		return nil, ErrBadTenantID
	}
	t := &Tenant{ID: id, Name: name}
	err := s.qrow(ctx, `INSERT INTO tenants (id, name) VALUES ($1,$2) RETURNING created_at`, id, name).Scan(&t.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return nil, ErrTenantExists
	}
	return t, err
}

func (s *Store) GetTenant(ctx context.Context, id string) (*Tenant, error) {
	t := &Tenant{}
	err := s.qrow(ctx, `SELECT id, name, created_at FROM tenants WHERE id=$1`, id).Scan(&t.ID, &t.Name, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (s *Store) ListTenants(ctx context.Context) ([]*Tenant, error) {
	rows, err := s.query(ctx, `SELECT id, name, created_at FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tenant
	for rows.Next() {
		t := &Tenant{}
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// tenantOfProject is the SQL every insert and every tenant-scoped read uses to
// name a project's tenant. $N is the project. One spelling, so the write side
// and the read side can never disagree.
func tenantOfProject(param string) string {
	return `COALESCE((SELECT tp.tenant_id FROM tenant_projects tp WHERE tp.project=` + param + `), '` + DefaultTenant + `')`
}

// ProjectTenant names the tenant a project belongs to.
func (s *Store) ProjectTenant(ctx context.Context, project string) (string, error) {
	var t string
	err := s.qrow(ctx, `SELECT `+tenantOfProject("$1"), project).Scan(&t)
	return t, err
}

// ProjectTenants names the tenant of every BOUND project; a project not in
// the map is the default tenant's. One query, so a request that decides
// visibility for a whole listing (#189) reads the bindings once.
func (s *Store) ProjectTenants(ctx context.Context) (map[string]string, error) {
	rows, err := s.query(ctx, `SELECT project, tenant_id FROM tenant_projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, t string
		if err := rows.Scan(&p, &t); err != nil {
			return nil, err
		}
		out[p] = t
	}
	return out, rows.Err()
}

// BindProject puts a project in a tenant and moves its existing runs and
// bundles with it, in one transaction. Binding a project to the tenant it is
// already in is a no-op; binding it to a different one is ErrProjectBound.
//
// A run inserted concurrently with a bind can be written with the old tenant.
// Reads are fail-closed against that: a tenant-scoped read requires the row's
// tenant AND the project's current tenant to match, so such a row is hidden
// from every tenant, never shown to the wrong one.
func (s *Store) BindProject(ctx context.Context, tenant, project string) error {
	if project == "" {
		return errors.New("project is required")
	}
	if _, err := s.GetTenant(ctx, tenant); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur string
	err = tx.QueryRowContext(ctx, s.d.q(`SELECT tenant_id FROM tenant_projects WHERE project=$1`), project).Scan(&cur)
	switch {
	case err == nil && cur == tenant:
		return nil
	case err == nil:
		return ErrProjectBound
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.ExecContext(ctx, s.d.q(`INSERT INTO tenant_projects (project, tenant_id) VALUES ($1,$2)`), project, tenant); err != nil {
		if isUniqueViolation(err) {
			return ErrProjectBound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, s.d.q(`UPDATE workflow_runs SET tenant_id=$1 WHERE project=$2`), tenant, project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.d.q(`UPDATE published_bundles SET tenant_id=$1 WHERE project=$2`), tenant, project); err != nil {
		return err
	}
	return tx.Commit()
}

// TenantProjects lists the projects bound to a tenant.
func (s *Store) TenantProjects(ctx context.Context, tenant string) ([]string, error) {
	rows, err := s.query(ctx, `SELECT project FROM tenant_projects WHERE tenant_id=$1 ORDER BY project`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- tenant-scoped run reads ----

// tenantRunWhere is the fail-closed condition (see BindProject).
func tenantRunWhere(param string) string {
	return `tenant_id=` + param + ` AND ` + tenantOfProject("workflow_runs.project") + `=` + param
}

// RunInTenant returns the run only if it belongs to the tenant. Absent and
// another tenant's are the same ErrNotFound.
func (s *Store) RunInTenant(ctx context.Context, tenant string, id uuid.UUID) (*Run, error) {
	r, err := scanRun(s.qrow(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE id=$1 AND `+tenantRunWhere("$2"), id, tenant))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// RunsInTenant lists a tenant's runs, newest first.
func (s *Store) RunsInTenant(ctx context.Context, tenant string, limit int) ([]*Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.query(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE `+tenantRunWhere("$1")+` ORDER BY created_at DESC LIMIT $2`, tenant, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- service principals ----

// ServicePrincipal is the METADATA of a machine caller bound to one tenant.
// There is no field for its token: the value exists once, in the answer that
// issued it, and is otherwise only a hash.
type ServicePrincipal struct {
	ID       uuid.UUID `json:"id"`
	TenantID string    `json:"tenant"`
	Name     string    `json:"name"`
	Prefix   string    `json:"prefix"`
	Endpoint string    `json:"endpoint"`
	Tools    []string  `json:"tools"`
	// Workflows are the names this principal may start. Empty = none.
	Workflows []string   `json:"workflows"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	LastUsed  *time.Time `json:"lastUsedAt,omitempty"`
}

var ErrPrincipalExists = errors.New("a principal with that name exists in this tenant")

// CreateServicePrincipal records a principal by the HASH of its token. The
// caller mints the token and hashes it (internal/auth); no value reaches SQL.
func (s *Store) CreateServicePrincipal(ctx context.Context, tenant, name, tokenHash, prefix string) (*ServicePrincipal, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name is required")
	}
	if _, err := s.GetTenant(ctx, tenant); err != nil {
		return nil, err
	}
	p := &ServicePrincipal{ID: uuid.New(), TenantID: tenant, Name: name, Prefix: prefix, Tools: []string{}, Workflows: []string{}}
	err := s.qrow(ctx, `INSERT INTO service_principals (id, tenant_id, name, token_hash, prefix) VALUES ($1,$2,$3,$4,$5) RETURNING created_at`,
		p.ID, tenant, name, tokenHash, prefix).Scan(&p.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return nil, ErrPrincipalExists
	}
	return p, err
}

const principalCols = `id, tenant_id, name, prefix, endpoint, created_at, revoked_at, last_used`

func scanPrincipal(row rowScanner) (*ServicePrincipal, error) {
	p := &ServicePrincipal{Tools: []string{}, Workflows: []string{}}
	var revoked, used sql.NullTime
	if err := row.Scan(&p.ID, &p.TenantID, &p.Name, &p.Prefix, &p.Endpoint, &p.CreatedAt, &revoked, &used); err != nil {
		return nil, err
	}
	if revoked.Valid {
		p.RevokedAt = &revoked.Time
	}
	if used.Valid {
		p.LastUsed = &used.Time
	}
	return p, nil
}

// ServicePrincipalByTokenHash resolves an ACTIVE principal in one statement:
// match on the hash and not revoked. Revoked, unknown and malformed are the
// same ErrNotFound.
func (s *Store) ServicePrincipalByTokenHash(ctx context.Context, hash string) (*ServicePrincipal, error) {
	p, err := scanPrincipal(s.qrow(ctx, `SELECT `+principalCols+` FROM service_principals WHERE token_hash=$1 AND revoked_at IS NULL`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_, _ = s.exec(ctx, `UPDATE service_principals SET last_used=now() WHERE id=$1`, p.ID)
	return p, s.loadTools(ctx, p)
}

// ServicePrincipalInTenant reads one principal of one tenant.
func (s *Store) ServicePrincipalInTenant(ctx context.Context, tenant string, id uuid.UUID) (*ServicePrincipal, error) {
	p, err := scanPrincipal(s.qrow(ctx, `SELECT `+principalCols+` FROM service_principals WHERE id=$1 AND tenant_id=$2`, id, tenant))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, s.loadTools(ctx, p)
}

func (s *Store) ListServicePrincipals(ctx context.Context, tenant string) ([]*ServicePrincipal, error) {
	rows, err := s.query(ctx, `SELECT `+principalCols+` FROM service_principals WHERE tenant_id=$1 ORDER BY name`, tenant)
	if err != nil {
		return nil, err
	}
	var out []*ServicePrincipal
	for rows.Next() {
		p, err := scanPrincipal(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range out {
		if err := s.loadTools(ctx, p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RevokeServicePrincipal stops a principal's token working on the next request.
func (s *Store) RevokeServicePrincipal(ctx context.Context, tenant string, id uuid.UUID) error {
	res, err := s.exec(ctx, `UPDATE service_principals SET revoked_at=now() WHERE id=$1 AND tenant_id=$2 AND revoked_at IS NULL`, id, tenant)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServicePrincipalSurface replaces a principal's declared surface: the
// endpoint its tools are reached at and their names. It is a reference list;
// nothing here calls it.
func (s *Store) SetServicePrincipalSurface(ctx context.Context, tenant string, id uuid.UUID, endpoint string, tools []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, s.d.q(`UPDATE service_principals SET endpoint=$1 WHERE id=$2 AND tenant_id=$3`), endpoint, id, tenant)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, s.d.q(`DELETE FROM service_principal_tools WHERE principal_id=$1`), id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, t := range tools {
		if seen[t] {
			continue
		}
		seen[t] = true
		if _, err := tx.ExecContext(ctx, s.d.q(`INSERT INTO service_principal_tools (principal_id, tool) VALUES ($1,$2)`), id, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// loadTools fills a principal's declared tools and its startable workflows.
func (s *Store) loadTools(ctx context.Context, p *ServicePrincipal) error {
	var err error
	if p.Tools, err = s.names(ctx, `SELECT tool FROM service_principal_tools WHERE principal_id=$1 ORDER BY tool`, p.ID); err != nil {
		return err
	}
	p.Workflows, err = s.names(ctx, `SELECT workflow FROM service_principal_workflows WHERE principal_id=$1 ORDER BY workflow`, p.ID)
	return err
}

func (s *Store) names(ctx context.Context, q string, id uuid.UUID) ([]string, error) {
	rows, err := s.query(ctx, q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetServicePrincipalWorkflows replaces the names a principal may start.
func (s *Store) SetServicePrincipalWorkflows(ctx context.Context, tenant string, id uuid.UUID, workflows []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var one int
	if err := tx.QueryRowContext(ctx, s.d.q(`SELECT 1 FROM service_principals WHERE id=$1 AND tenant_id=$2`), id, tenant).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, s.d.q(`DELETE FROM service_principal_workflows WHERE principal_id=$1`), id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, w := range workflows {
		if seen[w] {
			continue
		}
		seen[w] = true
		if _, err := tx.ExecContext(ctx, s.d.q(`INSERT INTO service_principal_workflows (principal_id, workflow) VALUES ($1,$2)`), id, w); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PrincipalMayStart reports whether an ACTIVE principal lists the workflow.
// Read from the database on every call, so a change applies to the next
// request, not the next token.
func (s *Store) PrincipalMayStart(ctx context.Context, id uuid.UUID, workflow string) (bool, error) {
	var one int
	err := s.qrow(ctx, `SELECT 1 FROM service_principal_workflows w JOIN service_principals p ON p.id = w.principal_id
		WHERE w.principal_id=$1 AND w.workflow=$2 AND p.revoked_at IS NULL`, id, workflow).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
