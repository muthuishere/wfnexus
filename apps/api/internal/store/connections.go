package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Tenant connections (migration 000034). The credential is stored only as
// the sealed bytes the caller hands in; this file never sees it in the clear
// and never returns it except to RunConnection, the engine's one reader.

type Connection struct {
	ID            uuid.UUID `json:"id"`
	TenantID      string    `json:"tenant"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Endpoint      string    `json:"endpoint"`
	HeaderName    string    `json:"headerName"`
	HeaderScheme  string    `json:"headerScheme"`
	HasCredential bool      `json:"hasCredential"`
	Tools         []string  `json:"tools"`
	CreatedAt     time.Time `json:"createdAt"`
	// credential is the SEALED value. Unexported, so it cannot be marshalled.
	credential []byte
}

// SealedCredential is the sealed credential, for the engine to open.
func (c *Connection) SealedCredential() []byte { return c.credential }

var ErrConnectionExists = errors.New("a connection with that name exists in this tenant")

// CreateConnection records a connection with an already-SEALED credential.
func (s *Store) CreateConnection(ctx context.Context, c *Connection, sealed []byte) error {
	if _, err := s.GetTenant(ctx, c.TenantID); err != nil {
		return err
	}
	c.ID = uuid.New()
	if c.Kind == "" {
		c.Kind = "mcp"
	}
	if c.HeaderName == "" {
		c.HeaderName = "Authorization"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, s.d.q(`INSERT INTO tenant_connections (id, tenant_id, name, kind, endpoint, header_name, header_scheme, credential_enc)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`),
		c.ID, c.TenantID, c.Name, c.Kind, c.Endpoint, c.HeaderName, c.HeaderScheme, sealed).Scan(&c.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConnectionExists
		}
		return err
	}
	if err := insertConnectionTools(ctx, s, tx, c.ID, c.Tools); err != nil {
		return err
	}
	c.HasCredential = len(sealed) > 0
	return tx.Commit()
}

func insertConnectionTools(ctx context.Context, s *Store, tx *sql.Tx, id uuid.UUID, tools []string) error {
	seen := map[string]bool{}
	for _, t := range tools {
		if seen[t] {
			continue
		}
		seen[t] = true
		if _, err := tx.ExecContext(ctx, s.d.q(`INSERT INTO tenant_connection_tools (connection_id, tool) VALUES ($1,$2)`), id, t); err != nil {
			return err
		}
	}
	return nil
}

const connCols = `id, tenant_id, name, kind, endpoint, header_name, header_scheme, credential_enc, created_at`

func (s *Store) scanConnection(ctx context.Context, row rowScanner) (*Connection, error) {
	c := &Connection{}
	if err := row.Scan(&c.ID, &c.TenantID, &c.Name, &c.Kind, &c.Endpoint, &c.HeaderName, &c.HeaderScheme, &c.credential, &c.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.HasCredential = len(c.credential) > 0
	return c, nil
}

func (s *Store) loadConnectionTools(ctx context.Context, c *Connection) error {
	var err error
	c.Tools, err = s.names(ctx, `SELECT tool FROM tenant_connection_tools WHERE connection_id=$1 ORDER BY tool`, c.ID)
	return err
}

// ConnectionInTenant reads one connection of one tenant, by name.
func (s *Store) ConnectionInTenant(ctx context.Context, tenant, name string) (*Connection, error) {
	c, err := s.scanConnection(ctx, s.qrow(ctx, `SELECT `+connCols+` FROM tenant_connections WHERE tenant_id=$1 AND name=$2`, tenant, name))
	if err != nil {
		return nil, err
	}
	return c, s.loadConnectionTools(ctx, c)
}

// ListConnections lists a tenant's connections (no credential).
func (s *Store) ListConnections(ctx context.Context, tenant string) ([]*Connection, error) {
	rows, err := s.query(ctx, `SELECT `+connCols+` FROM tenant_connections WHERE tenant_id=$1 ORDER BY name`, tenant)
	if err != nil {
		return nil, err
	}
	out := []*Connection{}
	for rows.Next() {
		c, err := s.scanConnection(ctx, rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		c.credential = nil
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range out {
		if err := s.loadConnectionTools(ctx, c); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SetConnectionTools replaces the tools a tenant's runs may call.
func (s *Store) SetConnectionTools(ctx context.Context, tenant, name string, tools []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id uuid.UUID
	if err := tx.QueryRowContext(ctx, s.d.q(`SELECT id FROM tenant_connections WHERE tenant_id=$1 AND name=$2`), tenant, name).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, s.d.q(`DELETE FROM tenant_connection_tools WHERE connection_id=$1`), id); err != nil {
		return err
	}
	if err := insertConnectionTools(ctx, s, tx, id, tools); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteConnection(ctx context.Context, tenant, name string) error {
	res, err := s.exec(ctx, `DELETE FROM tenant_connections WHERE tenant_id=$1 AND name=$2`, tenant, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RunTenant names the tenant a run belongs to, fail-closed: the row's tenant
// and its project's current tenant must agree, or ErrNotFound.
func (s *Store) RunTenant(ctx context.Context, id uuid.UUID) (string, error) {
	var t string
	err := s.qrow(ctx, `SELECT tenant_id FROM workflow_runs WHERE id=$1 AND tenant_id = `+tenantOfProject("workflow_runs.project"), id).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
}
