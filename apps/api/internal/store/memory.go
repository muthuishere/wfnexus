package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// Workflow memory (engine/memory.go): the records finished runs leave, the
// lessons agents write, and one rolling digest per workflow.

type MemoryFilter = model.MemoryFilter

// AddMemory writes one entry. ID and CreatedAt are filled when empty.
func (s *Store) AddMemory(ctx context.Context, m model.MemoryEntry) (model.MemoryEntry, error) {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	_, err := s.exec(ctx, `
		INSERT INTO workflow_memory (id, project, workflow, run_id, kind, status, step_id, body, created_ns)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		m.ID, m.Project, m.Workflow, m.RunID, m.Kind, m.Status, m.StepID, m.Body, m.CreatedAt.UnixNano())
	return m, err
}

// ListMemory returns matching entries, newest first.
func (s *Store) ListMemory(ctx context.Context, f MemoryFilter) ([]model.MemoryEntry, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if !f.AnyProject {
		add("project=$%d", f.Project)
	}
	if f.Workflow != "" {
		add("workflow=$%d", f.Workflow)
	}
	if f.NotWorkflow != "" {
		add("workflow<>$%d", f.NotWorkflow)
	}
	if f.Kind != "" {
		add("kind=$%d", f.Kind)
	}
	if f.RunID != "" {
		add("run_id=$%d", f.RunID)
	}
	q := `SELECT id, project, workflow, run_id, kind, status, step_id, body, created_ns FROM workflow_memory`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_ns DESC, id DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MemoryEntry{}
	for rows.Next() {
		var m model.MemoryEntry
		var ns int64
		if err := rows.Scan(&m.ID, &m.Project, &m.Workflow, &m.RunID, &m.Kind, &m.Status, &m.StepID, &m.Body, &ns); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(0, ns).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMemory removes one entry by id.
func (s *Store) DeleteMemory(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM workflow_memory WHERE id=$1`, id)
	return err
}

// ForgetMemory removes every entry of one workflow (in one project, or in all
// when anyProject). It returns how many went.
func (s *Store) ForgetMemory(ctx context.Context, project, workflow string, anyProject bool) (int64, error) {
	var (
		res interface{ RowsAffected() (int64, error) }
		err error
	)
	if anyProject {
		res, err = s.exec(ctx, `DELETE FROM workflow_memory WHERE workflow=$1`, workflow)
	} else {
		res, err = s.exec(ctx, `DELETE FROM workflow_memory WHERE workflow=$1 AND project=$2`, workflow, project)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
