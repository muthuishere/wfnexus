package engine

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	// Read the WHOLE skill, not just SKILL.md. A skill is a directory —
	// asserting against the entry file alone would fail the moment the method
	// is properly split into references and steps, which is what a skill this
	// size should do.
	dir := filepath.Dir(sk.Location)
	var whole strings.Builder
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		whole.Write(b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(whole.String())
	// The three things the loop cannot work without.
	for _, must := range []string{"wf_catalog", "wf_dryrun", "output_schema"} {
		if !strings.Contains(string(body), must) {
			t.Errorf("the skill never mentions %s", must)
		}
	}
	// The judgement calls, not just the field names: an author that reaches for
	// an agent every time is the expensive failure this skill exists to stop.
	for _, must := range []string{"judge", "decide", "run:", "cheapest"} {
		if !strings.Contains(string(body), must) {
			t.Errorf("the skill never mentions %s — nothing tells the author to use the cheap node", must)
		}
	}
	for _, f := range []string{
		"references/reference.md", "references/interview.md",
		"references/anti-patterns.md", "references/examples.md",
		"assets/routing.md",
		"assets/steps/step-01-interview.md", "assets/steps/step-edit.md",
		"assets/steps/step-diagnose.md", "assets/steps/step-convert.md",
		"assets/steps/step-review.md", "assets/steps/step-dryrun.md",
		"assets/steps/step-handover.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s is missing: %v", f, err)
		}
	}
	// The reference must carry the exact question types, because these are the
	// names an author cannot guess and a wrong one fails the whole file.
	ref, err := os.ReadFile(filepath.Join(dir, "references", "reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"noul", "choice", "score", "at_least", "skip_to", "requires_approval"} {
		if !strings.Contains(string(ref), must) {
			t.Errorf("the reference never mentions %q", must)
		}
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

// EVERY EXAMPLE MUST LOAD.
//
// An example that would not run is worse than no example: it is copied,
// it fails, and the person concludes the platform is broken rather than the
// documentation. They go through the same loader the server uses, so they
// cannot rot into something plausible.
func TestEveryAuthoringExampleLoads(t *testing.T) {
	dir := repoPath(t, filepath.Join("skills", "workflow-author", "assets", "examples"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("the examples directory is missing: %v", err)
	}
	reg := skills.Load(repoPath(t, "skills"))
	cat, err := catalog.Load(repoPath(t, "registries.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	defs, err := workflow.LoadDir(dir, catalog.NewValidator(reg, cat))
	if err != nil {
		t.Fatalf("the examples do not load: %v", err)
	}

	var yamls int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			yamls++
		}
	}
	if yamls < 8 {
		t.Errorf("only %d examples — the skill is meant to cover the range of shapes", yamls)
	}
	if len(defs) != yamls {
		t.Errorf("%d files but %d loaded definitions", yamls, len(defs))
	}

	// Between them they must demonstrate the shapes someone actually needs,
	// not nine variations of one.
	var sawRun, sawJudge, sawDecide, sawTeam, sawApproval, sawGoal, sawRunsOn, sawEnv, sawSchedule bool
	for _, d := range defs {
		if d.Goal != "" {
			sawGoal = true
		}
		if len(d.On.Schedule) > 0 {
			sawSchedule = true
		}
		if len(d.Env) > 0 {
			sawEnv = true
		}
		for i := range d.Steps {
			s := &d.Steps[i]
			switch {
			case s.Run != "":
				sawRun = true
			case s.Judge != nil:
				sawJudge = true
			}
			if s.Decide != nil {
				sawDecide = true
			}
			if len(s.Team) > 0 {
				sawTeam = true
			}
			if s.RequiresApproval {
				sawApproval = true
			}
			if s.RunsOn != "" {
				sawRunsOn = true
			}
		}
	}
	for name, seen := range map[string]bool{
		"a run: step": sawRun, "a judge: step": sawJudge, "a decide: block": sawDecide,
		"a team": sawTeam, "an approval gate": sawApproval, "a goal-planned workflow": sawGoal,
		"a runs-on label": sawRunsOn, "an env block": sawEnv, "a schedule": sawSchedule,
	} {
		if !seen {
			t.Errorf("no example demonstrates %s", name)
		}
	}
}

// THE LAYOUT CONVENTION. `references/` is flat prose a reader opens; `assets/`
// holds what the skill uses and may nest. Pinned because it is the kind of
// thing that drifts one file at a time and is never noticed until someone is
// looking for a step file in two places.
func TestSkillLayoutConvention(t *testing.T) {
	root := repoPath(t, filepath.Join("skills", "workflow-author"))

	refs, err := os.ReadDir(filepath.Join(root, "references"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range refs {
		if e.IsDir() {
			t.Errorf("references/%s is a directory — references is flat; nested material belongs in assets/", e.Name())
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			t.Errorf("references/%s is not prose — references is what a reader opens", e.Name())
		}
	}

	// And every path the skill points at must exist, or it sends its reader
	// somewhere that is not there.
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range skillRef.FindAllStringSubmatch(string(body), -1) {
			target := filepath.Join(root, m[1])
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s points at %s, which does not exist", filepath.Base(path), m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// skillRef matches a backticked path into the skill's own material.
var skillRef = regexp.MustCompile("`((?:assets|references)/[A-Za-z0-9._/-]+\\.(?:md|yaml))`")
