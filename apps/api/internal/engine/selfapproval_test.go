package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Segregation of duties, on SQLite (the no-config default): with
// prevent_self_approval, the person who started the run may not approve it —
// somebody else may, and the author is still recorded on the run.
func TestPreventSelfApprovalRefusesTheRunsAuthor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	def := &workflow.Definition{Name: "sod", Steps: []workflow.Step{{
		ID: "publish", Prompt: "x", RequiresApproval: true, PreventSelfApproval: true,
	}}}
	e := New(config.Config{WorkDir: t.TempDir()}, st, nil, map[string]*workflow.Definition{"sod": def}, skills.Load(), nil)

	run, _ := st.CreateRun(ctx, "local", "sod", []byte(`{}`))
	if err := st.SetTriggeredBy(ctx, run.ID, "ada"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetRun(ctx, run.ID); got.TriggeredBy != "ada" {
		t.Fatalf("triggered_by = %q, want ada", got.TriggeredBy)
	}
	_ = st.EnsureStep(ctx, run.ID, "publish", 0)
	_ = st.PatchStep(ctx, run.ID, "publish", model.StepPatch{Status: str("awaiting_approval")})
	_ = st.UpdateRun(ctx, run.ID, "awaiting_approval", "publish", "")

	err = e.Approve(ctx, run.ID, "publish", Actor{ID: "ada", Via: "api", Authenticated: true, Role: "admin"})
	if !errors.Is(err, ErrNotApprover) || !strings.Contains(err.Error(), "started this run") {
		t.Fatalf("the author approving their own run returned %v", err)
	}
	// Somebody else resolves it — a rejection, so no step has to run here.
	if err := e.Reject(ctx, run.ID, "publish", "not today", Actor{ID: "bob", Via: "api", Authenticated: true}); err != nil {
		t.Fatal(err)
	}
	steps, _ := st.ListSteps(ctx, run.ID)
	if len(steps) != 1 || steps[0].ResolvedBy != "bob (via api)" || steps[0].Resolution != "rejected" {
		t.Fatalf("steps = %+v", steps)
	}
}
