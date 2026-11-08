package store

import (
	"context"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// WorkflowRuns is the newest runs of one workflow in one project — the
// transcripts a memory dream checks against (engine/memory.go).
func (s *Store) WorkflowRuns(ctx context.Context, project, workflow string, limit int) ([]*model.Run, error) {
	return s.FindRuns(ctx, RunFilter{Project: project, Workflow: workflow, Limit: limit})
}
