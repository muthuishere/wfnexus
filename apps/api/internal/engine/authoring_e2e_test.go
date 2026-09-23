package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// AUTHORING, END TO END.
//
// The authoring workflow is the one every install runs before it has configured
// anything, and it is the only workflow whose output is another workflow — so a
// regression here is invisible until somebody's draft is silently wrong.
//
// These drive the real loop with a scripted model: the agent calls the platform
// tools, dry runs its own draft, and submits. Nothing is stubbed but the model.

// The method must live in the skill, not in the prompt. A paragraph inside one
// YAML file cannot be read, edited or reused by a step somebody writes later.
func TestAuthoringMethodIsASkill(t *testing.T) {
	reg := skills.Load(repoPath(t, "skills"))
	sk, ok := reg.Get("workflow-author")
	if !ok {
		t.Fatal("the workflow-author skill is not in the registry")
	}
	body, err := os.ReadFile(sk.Location)
	if err != nil {
		t.Fatal(err)
	}
	// The three things the loop cannot work without.
	for _, must := range []string{"wf_catalog", "wf_dryrun", "output_schema"} {
		if !strings.Contains(string(body), must) {
			t.Errorf("the skill never mentions %s", must)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(sk.Location), "reference.md")); err != nil {
		t.Errorf("the field reference is missing: %v", err)
	}
}

// It must run on the DEFAULT model. Pinning it to a provider that happens to be
// on one machine is how a fresh install finds the feature broken.
func TestAuthoringUsesTheDefaultModel(t *testing.T) {
	def := loadRepoWorkflow(t, "author-workflow")
	for i := range def.Steps {
		s := &def.Steps[i]
		if s.Provider != "" {
			t.Errorf("step %s pins provider %q; authoring must use the default model", s.ID, s.Provider)
		}
		if !hasString(s.Skills, "workflow-author") {
			t.Errorf("step %s does not load the workflow-author skill: %v", s.ID, s.Skills)
		}
	}
}

// The workflow that authors workflows must itself survive the check it applies
// to everything else.
func TestAuthoringWorkflowDryRunsClean(t *testing.T) {
	h := newHarness(t, loadRepoWorkflow(t, "author-workflow"), newFakeLLM(t), repoPath(t, "skills"))
	dry := h.eng.DryRunDefinition(h.eng.Definitions()["author-workflow"], map[string]any{
		"goal": "run the test suite and report what failed",
	})
	for _, p := range dry.Problems {
		if p.Fatal {
			t.Errorf("fatal: %s.%s — %s", p.Step, p.Field, p.Message)
		}
	}
	if !dry.OK {
		t.Fatal("the authoring workflow would not run here")
	}
	if dry.Cost.AgentSteps == 0 {
		t.Error("the dry run found no agent step")
	}
}

// The whole loop: the agent reads the shape, dry runs a draft, and submits a
// definition that actually loads. The model is scripted; everything else is the
// real engine, the real platform tools and the real validator.
func TestAuthoringProducesAWorkflowThatLoads(t *testing.T) {
	draft := map[string]any{
		"name":        "authored-demo",
		"description": "Run the suite and say what failed.",
		"on":          map[string]any{"workflow_dispatch": map[string]any{}},
		"steps": []any{map[string]any{
			"id":  "suite",
			"run": "go test ./...",
			"output_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ok": map[string]any{"type": "boolean"}, "exitCode": map[string]any{"type": "integer"},
					"stdout": map[string]any{"type": "string"}, "stderr": map[string]any{"type": "string"},
				},
			},
		}},
	}
	def := loadRepoWorkflow(t, "author-workflow")
	llm := newFakeLLM(t,
		callTool("wf_catalog", map[string]any{"kind": "shape"}),
		callTool("wf_dryrun", map[string]any{"definition": draft}),
		submit(map[string]any{
			"definition": draft, "dry_run_clean": true,
			"explanation": "One deterministic step. It does not fix anything.",
		}),
		finish(),
	)
	h := newHarness(t, def, llm, repoPath(t, "skills"))
	run := h.run(map[string]any{"goal": "run the suite and say what failed"})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}

	// The agent actually used the catalogue and the dry run — an author that
	// submits without checking is the failure the skill exists to prevent.
	var sawCatalog, sawDryRun bool
	for _, r := range llm.toolResults() {
		if strings.Contains(r, "steps") || strings.Contains(r, "shape") {
			sawCatalog = true
		}
		if strings.Contains(r, "waves") || strings.Contains(r, "would run") || strings.Contains(r, "\"ok\"") {
			sawDryRun = true
		}
	}
	if !sawCatalog || !sawDryRun {
		t.Errorf("the loop was not exercised: catalog=%v dryrun=%v results=%v", sawCatalog, sawDryRun, llm.toolResults())
	}

	// And what it submitted is a definition this platform would really load.
	steps, err := h.store.ListSteps(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Definition json.RawMessage `json:"definition"`
	}
	for _, s := range steps {
		if s.StepID == def.Steps[0].ID {
			_ = json.Unmarshal(s.Output, &out)
		}
	}
	if len(out.Definition) == 0 {
		t.Fatal("no definition came back")
	}
	var authored workflow.Definition
	if err := json.Unmarshal(out.Definition, &authored); err != nil {
		t.Fatalf("the submitted definition is not a definition: %v", err)
	}
	if err := workflow.Check(&authored, h.eng.validator()); err != nil {
		t.Fatalf("the authored workflow does not load: %v", err)
	}
}

// ---- helpers ----

// repoPath resolves a directory of the repository from inside the package.
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "..", rel)
}

// loadRepoWorkflow reads a workflow the repository actually ships, through the
// real loader — so these tests fail when that file changes rather than when a
// copy of it does.
func loadRepoWorkflow(t *testing.T, name string) *workflow.Definition {
	t.Helper()
	reg := skills.Load(repoPath(t, "skills"))
	cat, err := catalog.Load(repoPath(t, "registries.json"), "")
	if err != nil {
		t.Fatalf("registries: %v", err)
	}
	defs, err := workflow.LoadDir(repoPath(t, "workflows"), catalog.NewValidator(reg, cat))
	if err != nil {
		t.Fatalf("the shipped workflows do not load: %v", err)
	}
	def := defs[name]
	if def == nil {
		t.Fatalf("%s is not among the shipped workflows", name)
	}
	return def
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
