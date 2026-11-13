package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ProjectRow is a project's registry row (migration 000029). The project's
// workflows live on disk; the row is what metadata hangs on.
type ProjectRow struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// RegisterProject records a project. Registering one that exists is a no-op,
// so boot and create can both call it.
func (s *Store) RegisterProject(ctx context.Context, name string) error {
	_, err := s.exec(ctx, `INSERT INTO projects (name, created_ns) VALUES ($1, $2)
		ON CONFLICT (name) DO NOTHING`, name, time.Now().UnixNano())
	return err
}

// UnregisterProject removes a project's registry row.
func (s *Store) UnregisterProject(ctx context.Context, name string) error {
	_, err := s.exec(ctx, `DELETE FROM projects WHERE name=$1`, name)
	return err
}

// GetProjectRow returns the registry row, or sql.ErrNoRows.
func (s *Store) GetProjectRow(ctx context.Context, name string) (*ProjectRow, error) {
	var p ProjectRow
	var c int64
	if err := s.qrow(ctx, `SELECT name, created_ns FROM projects WHERE name=$1`, name).Scan(&p.Name, &c); err != nil {
		return nil, err
	}
	p.CreatedAt = time.Unix(0, c).UTC()
	return &p, nil
}

// ProjectRegistered reports whether a project has a registry row.
func (s *Store) ProjectRegistered(ctx context.Context, name string) (bool, error) {
	_, err := s.GetProjectRow(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ListProjectRows lists every registered project, by name.
func (s *Store) ListProjectRows(ctx context.Context) ([]ProjectRow, error) {
	rows, err := s.query(ctx, `SELECT name, created_ns FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		var p ProjectRow
		var c int64
		if err := rows.Scan(&p.Name, &c); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(0, c).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}
