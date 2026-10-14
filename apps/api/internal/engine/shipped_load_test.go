package engine

import (
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Every shipped workflow and template loads under the strict loader (unknown
// keys refused) against the repo's own skills and registries, with nothing
// skipped. This is `wfx validate` over workflows/ and templates/, in CI.
func TestEveryShippedWorkflowAndTemplateLoads(t *testing.T) {
	reg := skills.Load(repoPath(t, "skills"))
	cat, err := catalog.Load(repoPath(t, "registries.json"), "")
	if err != nil {
		t.Fatalf("registries: %v", err)
	}
	defs, skips, err := workflow.LoadSources([]workflow.Source{
		{Name: "workflows", Dir: repoPath(t, "workflows")},
		{Name: "templates", Dir: repoPath(t, "templates")},
	}, catalog.NewValidator(reg, cat))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skips {
		t.Errorf("%s/%s did not load: %s", s.Source, s.Workflow, s.Reason)
	}
	if len(defs) == 0 {
		t.Fatal("nothing loaded")
	}
	t.Logf("%d workflows loaded", len(workflow.Sorted(defs)))
}
