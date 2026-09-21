package engine

import (
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A `run` node costs nothing: no model is called, and its output is a fact the
// rest of the workflow reads like any other.
func TestRunNodeCallsNoModel(t *testing.T) {
	def := &workflow.Definition{Name: "cmd", Steps: []workflow.Step{
		{ID: "hello", Run: "echo wfnexus", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	llm := newFakeLLM(t)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if llm.calls() != 0 {
		t.Fatalf("a run node must call no model, got %d calls", llm.calls())
	}
	out := output(t, h.steps(run.ID)["hello"])
	if out["ok"] != true {
		t.Fatalf("output = %v", out)
	}
	if s, _ := out["stdout"].(string); !strings.Contains(s, "wfnexus") {
		t.Fatalf("stdout not captured: %q", s)
	}
}

// A non-zero exit is the command's ANSWER, not a platform failure: the step
// succeeds and the workflow decides what it means.
func TestRunNodeFailingCommandIsAFactNotACrash(t *testing.T) {
	def := &workflow.Definition{Name: "redsuite", Steps: []workflow.Step{
		{ID: "tests", Run: "exit 3", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)

	if run.Status != "done" {
		t.Fatalf("a non-zero exit should not fail the run: %s (%s)", run.Status, run.Error)
	}
	out := output(t, h.steps(run.ID)["tests"])
	if out["ok"] != false {
		t.Fatalf("ok = %v, want false", out["ok"])
	}
	if got, _ := out["exitCode"].(float64); got != 3 {
		t.Fatalf("exitCode = %v, want 3", out["exitCode"])
	}
}

// …and a gate turns that fact into a decision, exactly as for an agent's output.
func TestRunNodeGateStopsTheRun(t *testing.T) {
	def := &workflow.Definition{Name: "gated", Steps: []workflow.Step{
		{
			ID: "tests", Run: "exit 1", OutputSchema: workflow.RunOutputSchema(),
			Gates: []workflow.Gate{{Field: "ok", Equals: false, Action: "fail", Message: "the suite is red"}},
		},
		{ID: "after", Run: "echo never", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)

	if run.Status != "failed" || run.Error != "the suite is red" {
		t.Fatalf("run = %s (%q)", run.Status, run.Error)
	}
	if st := h.steps(run.ID)["after"]; st.Status == "done" {
		t.Fatal("a step after a failed gate ran")
	}
}

// A run node is contained like everything else: it executes in the run's
// workspace, not wherever the server happens to live.
func TestRunNodeExecutesInTheWorkspace(t *testing.T) {
	ws := t.TempDir()
	def := &workflow.Definition{Name: "where", Steps: []workflow.Step{
		{ID: "pwd", Run: "pwd", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(map[string]any{"repo_path": ws, "isolate": false})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	out, _ := output(t, h.steps(run.ID)["pwd"])["stdout"].(string)
	if !strings.Contains(out, strings.TrimPrefix(ws, "/private")) {
		t.Fatalf("ran in the wrong directory: %q, want %q", out, ws)
	}
}

// A judge node is a decision and nothing else — no agent, no tools — and its
// answers are validated facts.
func TestJudgeNodeProducesFactsWithoutAnAgent(t *testing.T) {
	judge := &workflow.Decide{
		State: "a report about money going missing",
		Questions: map[string]workflow.Question{
			"desk": {
				Type: "choice", Instructions: "which desk owns this",
				Options: map[string]string{
					"billing":  "own it here when the problem is money - charges, refunds, invoices",
					"shipping": "own it here when the problem is delivery - damage in transit, late parcels",
				},
			},
		},
	}
	def := &workflow.Definition{Name: "route", Steps: []workflow.Step{
		{ID: "route", Judge: judge},
	}}
	normalizeForTest(def)
	if def.Steps[0].OutputSchema == nil {
		def.Steps[0].OutputSchema = workflow.JudgeOutputSchema(judge)
	}

	llm := newFakeLLM(t)
	h := newHarness(t, def, llm, "")
	h.eng.UseClassifier(recordDecision(t, &def.Steps[0], 0, 0))

	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if llm.calls() != 0 {
		t.Fatalf("a judge node must not call an agent, got %d", llm.calls())
	}
	if got := output(t, h.steps(run.ID)["route"])["desk"]; got != "billing" {
		t.Fatalf("desk = %v", got)
	}
}

// The derived schema is the contract: a choice answer must be one of the
// options offered.
func TestJudgeOutputSchemaConstrainsAnswers(t *testing.T) {
	sch := workflow.JudgeOutputSchema(&workflow.Decide{Questions: map[string]workflow.Question{
		"desk":   {Type: "choice", Options: map[string]string{"a": "x", "b": "y"}},
		"risk":   {Type: "score", Levels: []string{"low", "mid", "high"}},
		"is_sec": {Type: "noul"},
	}})
	props := sch["properties"].(map[string]any)
	desk := props["desk"].(map[string]any)
	if got := desk["enum"].([]any); len(got) != 2 {
		t.Fatalf("choice enum = %v", got)
	}
	if props["risk"].(map[string]any)["maximum"] != float64(2) {
		t.Fatalf("score maximum should be the last level index: %v", props["risk"])
	}
	if props["is_sec"].(map[string]any)["maximum"] != 1 {
		t.Fatalf("a noul is 0..1: %v", props["is_sec"])
	}
	if len(sch["required"].([]any)) != 3 {
		t.Fatalf("every question is required: %v", sch["required"])
	}
}
