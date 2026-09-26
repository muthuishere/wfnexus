package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A restart used to leave every in-flight run "running" forever. At boot, a
// run still running on a step this process executes is marked failed, saying
// why; a run paused for a person is not touched.
func TestARestartMarksCutOffRunsAndLeavesPausedOnes(t *testing.T) {
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
	def := &workflow.Definition{Name: "gen", Steps: []workflow.Step{{ID: "map", Prompt: "x"}}}
	e := New(config.Config{WorkDir: t.TempDir()}, st, nil, map[string]*workflow.Definition{"gen": def}, skills.Load(), nil)

	cut, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
	_ = st.EnsureStep(ctx, cut.ID, "map", 0)
	_ = st.UpdateRun(ctx, cut.ID, "running", "map", "")
	paused, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
	_ = st.UpdateRun(ctx, paused.ID, "awaiting_approval", "map", "")

	if n := e.MarkInterrupted(ctx); n != 1 {
		t.Fatalf("marked %d runs, want 1", n)
	}
	got, _ := st.GetRun(ctx, cut.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "server restart") {
		t.Fatalf("cut-off run: %s %q", got.Status, got.Error)
	}
	if p, _ := st.GetRun(ctx, paused.ID); p.Status != "awaiting_approval" {
		t.Fatalf("a paused run was touched: %s", p.Status)
	}
}
