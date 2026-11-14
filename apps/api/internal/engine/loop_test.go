package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// loopDef parses and checks a workflow the way the loader does.
func loopDef(t *testing.T, src string) *workflow.Definition {
	t.Helper()
	d := &workflow.Definition{}
	if err := yaml.Unmarshal([]byte(src), d); err != nil {
		t.Fatal(err)
	}
	if err := workflow.Check(d, nil); err != nil {
		t.Fatalf("definition rejected: %v", err)
	}
	return d
}

func outputOf(t *testing.T, st *store.StepRun) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(st.Output, &m); err != nil {
		t.Fatalf("%s output: %v (%s)", st.StepID, err, st.Output)
	}
	return m
}

// foreach over an input list runs one persisted row per item, in parallel up
// to the cap, and the loop's own output is the array of their outputs in
// item order — whatever order they finished in.
func TestForeachRunsOneRowPerItemAndAggregates(t *testing.T) {
	def := loopDef(t, `
name: foreach-input
input_schema:
  type: object
  properties:
    files: {type: array, items: {type: string}}
steps:
  - id: review
    foreach: input.files
    parallel: 2
    run: echo "{{ .Index }}={{ .Item }}"
  - id: summary
    run: echo "{{ .Steps.review.count }} reviewed"
`)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(map[string]any{"files": []any{"a.go", "b.go", "c.go"}})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	for i, f := range []string{"a.go", "b.go", "c.go"} {
		row := steps[workflow.IterationID("review", i, "")]
		if row == nil || row.Status != "done" {
			t.Fatalf("iteration %d row missing or not done: %+v", i, row)
		}
		if got := outputOf(t, row)["stdout"].(string); !strings.Contains(got, f) {
			t.Fatalf("iteration %d saw %q, want item %s", i, got, f)
		}
	}
	agg := outputOf(t, steps["review"])
	its := agg["iterations"].([]any)
	if agg["count"].(float64) != 3 || len(its) != 3 {
		t.Fatalf("aggregate = %v", agg)
	}
	if !strings.Contains(its[2].(map[string]any)["stdout"].(string), "2=c.go") {
		t.Fatalf("aggregate is not in item order: %v", its)
	}
	if !strings.Contains(outputOf(t, steps["summary"])["stdout"].(string), "3 reviewed") {
		t.Fatalf("a later step must read the aggregate: %v", outputOf(t, steps["summary"]))
	}
}

// until repeats a body — fix, then check — until the check passes, and the
// next iteration's fix can read the previous check.
func TestUntilRepeatsBodyUntilCheckPasses(t *testing.T) {
	def := loopDef(t, `
name: until-green
steps:
  - id: fix-loop
    max_iterations: 5
    until:
      - path: check.exitCode
        equals: 0
    steps:
      - id: fix
        run: echo x >> attempts.txt; echo "prev={{ .Steps.check.exitCode }}"
      - id: check
        run: test "$(wc -l < attempts.txt | tr -d ' ')" -ge 3
`)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(map[string]any{"isolate": false})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	agg := outputOf(t, steps["fix-loop"])
	if agg["count"].(float64) != 3 {
		t.Fatalf("want 3 iterations, got %v", agg["count"])
	}
	if steps["fix-loop[3].fix"] != nil {
		t.Fatal("no row may exist for an iteration that never ran")
	}
	if got := outputOf(t, steps["fix-loop[1].fix"])["stdout"].(string); !strings.Contains(got, "prev=1") {
		t.Fatalf("iteration 1 must see iteration 0's failed check: %q", got)
	}
	last := agg["last"].(map[string]any)["check"].(map[string]any)
	if last["exitCode"].(float64) != 0 {
		t.Fatalf("last = %v", last)
	}
}

// A loop that runs out of iterations without its condition settling FAILS —
// it does not quietly carry on as if it had worked.
func TestUntilExhaustedFailsTheRun(t *testing.T) {
	def := loopDef(t, `
name: until-never
steps:
  - id: poll
    max_iterations: 2
    run: "exit 1"
    until:
      - path: poll.exitCode
        equals: 0
  - id: after
    run: echo never
`)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "failed" || !strings.Contains(run.Error, "not met after 2 iterations") {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if steps["poll"].Status != "failed" || steps["poll[1]"].Status != "done" || steps["after"].Status == "done" {
		t.Fatalf("poll=%s poll[1]=%s after=%s", steps["poll"].Status, steps["poll[1]"].Status, steps["after"].Status)
	}
}

// requires_approval on a single-step loop is asked PER ITERATION, and an
// approval resumes the loop where it stopped without re-running what was done.
func TestApprovalInsideALoopPausesEachIteration(t *testing.T) {
	def := loopDef(t, `
name: approve-each
steps:
  - id: deploy
    foreach: [eu, us]
    requires_approval: true
    run: echo "deploy {{ .Item }}" >> /dev/null; echo "{{ .Item }}"
`)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "awaiting_approval" || run.CurrentStep != "deploy[0]" {
		t.Fatalf("run = %s at %q", run.Status, run.CurrentStep)
	}
	if st := h.steps(run.ID)["deploy[1]"]; st.Status != "pending" {
		t.Fatalf("the second iteration must wait: %s", st.Status)
	}
	if err := h.eng.Approve(context.Background(), run.ID, "deploy[0]", Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "awaiting_approval" || run.CurrentStep != "deploy[1]" {
		t.Fatalf("after first approval run = %s at %q", run.Status, run.CurrentStep)
	}
	if err := h.eng.Approve(context.Background(), run.ID, "deploy[1]", Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if steps["deploy[0]"].ResolvedBy == "" || steps["deploy[1]"].ResolvedBy == "" {
		t.Fatal("each iteration's approval must be recorded on its own row")
	}
	if outputOf(t, steps["deploy"])["count"].(float64) != 2 {
		t.Fatalf("aggregate = %s", steps["deploy"].Output)
	}
}

// A loop in a DAG iterates over an earlier step's output.
func TestForeachInADAGOverAStepOutput(t *testing.T) {
	def := loopDef(t, `
name: dag-foreach
steps:
  - id: list
    prompt: list the services
    tools: [bash]
    output_schema:
      type: object
      required: [services]
      properties:
        services: {type: array, items: {type: string}}
  - id: probe
    needs: [list]
    foreach: list.services
    parallel: 3
    steps:
      - id: ping
        run: echo "ping {{ .Item }}"
      - id: note
        run: echo "{{ .Steps.ping.stdout }}"
`)
	llm := newFakeLLM(t, submit(map[string]any{"services": []any{"api", "db"}}), finish())
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if got := outputOf(t, steps["probe[1].note"])["stdout"].(string); !strings.Contains(got, "ping db") {
		t.Fatalf("a body step must read the earlier body step of its own iteration: %q", got)
	}
	if outputOf(t, steps["probe"])["count"].(float64) != 2 {
		t.Fatalf("aggregate = %s", steps["probe"].Output)
	}
}

// The dry run lists the iterations a loop would plan, and checks a body
// step's templates as its first iteration would render them.
func TestDryRunShowsPlannedIterations(t *testing.T) {
	def := loopDef(t, `
name: dry-loops
input_schema:
  type: object
  properties:
    files: {type: array, items: {type: string}, default: [a, b, c]}
steps:
  - id: review
    foreach: input.files
    run: echo "{{ .Item }}"
  - id: fix-loop
    max_iterations: 3
    until: [{path: check.ok, equals: true}]
    steps:
      - id: fix
        run: echo "{{ .Steps.check.stdout }}"
      - id: check
        run: "true"
  - id: after
    run: echo "{{ .Steps.review.count }} {{ .Steps.fix.stdout }}"
`)
	h := newHarness(t, def, newFakeLLM(t), "")
	dr := h.eng.DryRunDefinition(def, nil)
	byID := map[string]DryRunStep{}
	for _, s := range dr.Steps {
		byID[s.ID] = s
	}
	rv := byID["review"].Loop
	if rv == nil || rv.Items != 3 || len(rv.Iterations) != 3 || rv.Iterations[2] != "review[2]" {
		t.Fatalf("review loop = %+v", rv)
	}
	if !strings.Contains(byID["review"].Prompt, "a") {
		t.Fatalf("the first iteration's render: %q", byID["review"].Prompt)
	}
	fl := byID["fix-loop"].Loop
	if fl == nil || fl.Mode != "until" || len(fl.Iterations) != 6 || len(fl.Body) != 2 {
		t.Fatalf("fix-loop = %+v", fl)
	}
	var fatal []string
	for _, p := range dr.Problems {
		if p.Fatal {
			fatal = append(fatal, p.Step+": "+p.Message)
		}
	}
	// The ONLY fatal reference is `after` reading a body step from outside.
	if len(fatal) != 1 || !strings.Contains(fatal[0], `"fix"`) {
		t.Fatalf("fatal problems = %v", fatal)
	}
}

// The shipped loop template dry-runs clean: its loop is planned round by
// round, and no template in it reads anything that would render empty.
func TestFixUntilGreenTemplateDryRuns(t *testing.T) {
	reg := skills.Load(repoPath(t, "skills"))
	cat, err := catalog.Load(repoPath(t, "registries.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	defs, _, err := workflow.LoadSources([]workflow.Source{{Name: "templates", Dir: repoPath(t, "templates")}}, catalog.NewValidator(reg, cat), shippedModules(t))
	if err != nil {
		t.Fatal(err)
	}
	var def *workflow.Definition
	for _, d := range defs {
		if d.Name == "fix-until-green" {
			def = d
		}
	}
	if def == nil {
		t.Fatal("fix-until-green did not load")
	}
	h := newHarness(t, def, newFakeLLM(t), "")
	dr := h.eng.DryRunDefinition(def, nil)
	for _, p := range dr.Problems {
		if p.Fatal && p.Field != "provider" && p.Field != "env" {
			t.Errorf("fatal: %s %s: %s", p.Step, p.Field, p.Message)
		}
	}
	for _, s := range dr.Steps {
		if s.ID == "repair" {
			if s.Loop == nil || len(s.Loop.Iterations) != 8 || s.Loop.Until != "check.ok = true" {
				t.Fatalf("repair loop = %+v", s.Loop)
			}
			if !strings.Contains(s.Loop.Body[0].Prompt, "Round 0 of at most 4") {
				t.Fatalf("body prompt as round 0 renders it: %q", s.Loop.Body[0].Prompt)
			}
			return
		}
	}
	t.Fatal("no repair step in the dry run")
}
