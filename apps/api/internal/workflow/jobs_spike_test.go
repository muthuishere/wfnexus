package workflow

import (
	"strings"
	"testing"
)

// SPIKE: does the GitHub Actions shape actually fit everything wfnexus needs?
//
// Each case is a question we could not answer by reading. A case that FAILS is
// the finding — it means the syntax does not fit that shape and we should not
// ship it for that case.

func loadJobs(t *testing.T, body string) (*Definition, error) {
	t.Helper()
	dir := t.TempDir()
	writeWorkflow(t, dir, "x.yaml", body)
	defs, err := LoadDir(dir, catalog())
	if err != nil {
		return nil, err
	}
	return defs["spike"], nil
}

func stepIDs(d *Definition) []string {
	out := make([]string, 0, len(d.Steps))
	for _, s := range d.Steps {
		out = append(out, s.ID)
	}
	return out
}

func stepByID(d *Definition, id string) *Step {
	for i := range d.Steps {
		if d.Steps[i].ID == id {
			return &d.Steps[i]
		}
	}
	return nil
}

const outSchema = `        output_schema: {type: object, required: [ok], properties: {ok: {type: boolean}}}`

// S1 — the core claim: jobs are the scheduling unit, steps the work unit.
// A job's steps must chain; two independent jobs must NOT chain.
func TestSpikeS1_JobsFlattenWithTheRightEdges(t *testing.T) {
	d, err := loadJobs(t, `
name: spike
jobs:
  build:
    steps:
      - {id: compile, prompt: p,
`+outSchema[8:]+`}
      - {id: package, prompt: p,
`+outSchema[8:]+`}
  test:
    needs: [build]
    steps:
      - {id: unit, prompt: p,
`+outSchema[8:]+`}
`)
	if err != nil {
		t.Fatalf("FINDING: the basic jobs shape does not load: %v", err)
	}
	want := []string{"build.compile", "build.package", "test.unit"}
	got := stepIDs(d)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("flattened ids = %v, want %v", got, want)
	}
	if n := stepByID(d, "build.package").Needs; len(n) != 1 || n[0] != "build.compile" {
		t.Fatalf("steps inside a job must chain; build.package needs = %v", n)
	}
	if n := stepByID(d, "build.compile").Needs; len(n) != 0 {
		t.Fatalf("a job's first step must not depend on anything; got %v", n)
	}
	// job-level needs: test depends on build's LAST step, so the job is one node
	if n := stepByID(d, "test.unit").Needs; len(n) != 1 || n[0] != "build.package" {
		t.Fatalf("job needs must attach to the dependency's last step; got %v", n)
	}
}

// S2 — a job's `if` must guard the whole job, not just its first step.
func TestSpikeS2_JobIfGuardsEveryStep(t *testing.T) {
	d, err := loadJobs(t, `
name: spike
jobs:
  maybe:
    if:
      - {path: input.deploy, equals: true}
    steps:
      - {id: one, prompt: p,
`+outSchema[8:]+`}
      - {id: two, prompt: p,
`+outSchema[8:]+`}
`)
	if err != nil {
		t.Fatalf("FINDING: job-level `if` does not load: %v", err)
	}
	for _, id := range []string{"maybe.one", "maybe.two"} {
		if len(stepByID(d, id).When) != 1 {
			t.Fatalf("FINDING: job `if` did not reach %s — a skipped job would run half of itself", id)
		}
	}
}

// S3 — job `defaults` must be a FLOOR, never an override: a step that chose a
// provider keeps it.
func TestSpikeS3_DefaultsDoNotOverrideAStepsOwnChoice(t *testing.T) {
	d, err := loadJobs(t, `
name: spike
jobs:
  work:
    defaults:
      provider: sonnet
      add_tools: [read]
    steps:
      - {id: inherits, prompt: p,
`+outSchema[8:]+`}
      - {id: chooses, prompt: p, provider: devin, tools: [bash],
`+outSchema[8:]+`}
`)
	if err != nil {
		t.Fatalf("FINDING: job defaults do not load: %v", err)
	}
	if got := stepByID(d, "work.inherits").Provider; got != "sonnet" {
		t.Fatalf("default provider did not apply: %q", got)
	}
	if got := stepByID(d, "work.chooses").Provider; got != "devin" {
		t.Fatalf("FINDING: defaults overrode a step's own provider (%q) — defaults must be a floor", got)
	}
	if tools := stepByID(d, "work.chooses").Tools; len(tools) != 2 {
		t.Fatalf("defaults should ADD tools, not replace: %v", tools)
	}
}

// S4 — THE HARD ONE. Can the Actions shape coexist with a DERIVED plan?
// A job declares the facts it consumes and produces; the goal drives the order
// and `needs` is not written at all.
func TestSpikeS4_JobsCanParticipateInADerivedPlan(t *testing.T) {
	d, err := loadJobs(t, `
name: spike
goal: shipped
jobs:
  triage:
    consumes: [report]
    produces: [triage]
    steps:
      - {id: classify, prompt: p,
`+outSchema[8:]+`}
  ship:
    consumes: [triage]
    produces: [shipped]
    steps:
      - {id: build, prompt: p,
`+outSchema[8:]+`}
      - {id: publish, prompt: p,
`+outSchema[8:]+`}
`)
	if err != nil {
		t.Fatalf("FINDING: jobs and a derived plan cannot coexist: %v", err)
	}
	if !d.IsPlanned() {
		t.Fatal("FINDING: a workflow with job-level facts is not recognised as planned")
	}
	// a job's facts must sit at its BOUNDARY: consumed by the first step,
	// produced by the last — otherwise the job is not one node to the planner
	if c := stepByID(d, "ship.build").Consumes; len(c) != 1 || c[0] != "triage" {
		t.Fatalf("job consumes must attach to its FIRST step: %v", c)
	}
	if p := stepByID(d, "ship.publish").Produces; len(p) != 1 || p[0] != "shipped" {
		t.Fatalf("job produces must attach to its LAST step: %v", p)
	}
	if p := stepByID(d, "ship.build").Produces; len(p) != 0 {
		t.Fatalf("a job's middle step must not produce the job's fact: %v", p)
	}
}

// S5 — mixing the two ordering models in one workflow must be REFUSED, not
// silently half-honoured.
func TestSpikeS5_NeedsAndFactsCannotBeMixed(t *testing.T) {
	_, err := loadJobs(t, `
name: spike
goal: done
jobs:
  a:
    produces: [done]
    steps:
      - {id: x, prompt: p,
`+outSchema[8:]+`}
  b:
    needs: [a]
    steps:
      - {id: y, prompt: p,
`+outSchema[8:]+`}
`)
	if err == nil {
		t.Fatal("FINDING: a workflow mixing job `needs` with a derived plan was accepted — the two will disagree")
	}
	if !strings.Contains(err.Error(), "needs") {
		t.Fatalf("the refusal should explain the conflict: %v", err)
	}
}

// S6 — reuse: `uses` + `with` inside a job, with per-use capability grants.
func TestSpikeS6_TaskReuseInsideAJob(t *testing.T) {
	dir := t.TempDir()
	tasks := t.TempDir()
	writeWorkflow(t, tasks, "repro.yaml", `
name: reproduce
steps:
  - id: run
    prompt: reproduce it
    tools: [bash]
    consumes: [triage]
    produces: [reproduction]
    output_schema:
      type: object
      required: [ok]
      properties:
        ok: {type: boolean}
`)
	writeWorkflow(t, dir, "x.yaml", `
name: spike
jobs:
  repro:
    uses:
      - use: reproduce
        as: r
        with:
          "*":
            add_skills: [fix-author]
            add_guardrails:
              - {deny: bash, args_contain: ["git push"], reason: reproduction never publishes}
        consumes: {triage: classification}
`)
	defs, err := LoadDirWithTasks(dir, tasks, catalog())
	if err != nil {
		t.Fatalf("FINDING: task reuse inside a job does not load: %v", err)
	}
	d := defs["spike"]
	s := stepByID(d, "repro.r.run")
	if s == nil {
		t.Fatalf("FINDING: expanded id is not job.use.step — got %v", stepIDs(d))
	}
	if len(s.Skills) != 1 || s.Skills[0] != "fix-author" {
		t.Fatalf("with.add_skills did not apply: %v", s.Skills)
	}
	if len(s.Guardrails) != 1 {
		t.Fatalf("FINDING: a use could not tighten policy: %v", s.Guardrails)
	}
	if len(s.Consumes) != 1 || s.Consumes[0] != "classification" {
		t.Fatalf("FINDING: fact renaming at the boundary did not apply: %v", s.Consumes)
	}
}

// S7 — a job with no steps is a typo, not an empty success.
func TestSpikeS7_EmptyJobIsRefused(t *testing.T) {
	if _, err := loadJobs(t, "name: spike\njobs:\n  hollow:\n    needs: []\n"); err == nil {
		t.Fatal("FINDING: an empty job was accepted")
	}
}

// S8 — jobs and a flat step list in one file must be refused: two ordering
// models in one document is how a reader ends up wrong about what runs.
func TestSpikeS8_JobsAndFlatStepsAreRefused(t *testing.T) {
	_, err := loadJobs(t, `
name: spike
jobs:
  a:
    steps:
      - {id: x, prompt: p,
`+outSchema[8:]+`}
steps:
  - {id: loose, prompt: p,
`+outSchema[8:]+`}
`)
	if err == nil {
		t.Fatal("FINDING: a workflow with both jobs and flat steps was accepted")
	}
}

// S9 — the existing flat form must keep working untouched. Adopting a new
// syntax must not invalidate the three workflows already shipped.
func TestSpikeS9_FlatStepsStillLoad(t *testing.T) {
	d, err := loadJobs(t, "name: spike\nsteps:\n  - {id: only, prompt: p, tools: [bash],\n"+outSchema[8:]+"}\n")
	if err != nil {
		t.Fatalf("FINDING: the flat form regressed: %v", err)
	}
	if len(d.Steps) != 1 || d.Steps[0].ID != "only" {
		t.Fatalf("flat ids must NOT be prefixed: %v", stepIDs(d))
	}
	if d.Steps[0].JobID != "" {
		t.Fatalf("a flat step must carry no job id: %q", d.Steps[0].JobID)
	}
}
