package engine

import (
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A watch that exits 3 for "findings" must reach its triage step, not crash
// the run: exit-codes declares the exit as an answer.
func TestExitCodesDeclaredExitIsAnAnswer(t *testing.T) {
	def := &workflow.Definition{Name: "watch", Steps: []workflow.Step{
		{ID: "check", Run: "exit 3", ExitCodes: map[int]string{0: "ok", 3: "findings"}, OutputSchema: workflow.RunOutputSchema()},
		{ID: "triage", Needs: []string{"check"}, Run: "echo triage", OutputSchema: workflow.RunOutputSchema(),
			When: []workflow.Guard{{Path: "check.outcome", Equals: "findings"}}},
		{ID: "quiet", Needs: []string{"check"}, Run: "echo quiet", OutputSchema: workflow.RunOutputSchema(),
			When: []workflow.Guard{{Path: "check.outcome", Equals: "ok"}}},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	out := output(t, steps["check"])
	if steps["check"].Status != "done" || out["outcome"] != "findings" || out["ok"] != true {
		t.Fatalf("check = %s %v", steps["check"].Status, out)
	}
	if got, _ := out["exitCode"].(float64); got != 3 {
		t.Fatalf("exitCode = %v", out["exitCode"])
	}
	if steps["triage"].Status != "done" {
		t.Fatalf("triage = %s, want done", steps["triage"].Status)
	}
	if steps["quiet"].Status != "skipped" {
		t.Fatalf("quiet = %s, want skipped", steps["quiet"].Status)
	}
}

// An exit the step did not declare is still a crash.
func TestExitCodesUndeclaredExitStillFails(t *testing.T) {
	def := &workflow.Definition{Name: "watch", Steps: []workflow.Step{
		{ID: "check", Run: "exit 2", ExitCodes: map[int]string{0: "ok", 3: "findings"}, OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "failed" || !strings.Contains(run.Error, "exited 2") {
		t.Fatalf("run = %s (%q), want failed on exit 2", run.Status, run.Error)
	}
	if _, has := output(t, h.steps(run.ID)["check"])["outcome"]; has {
		t.Fatal("an undeclared exit got an outcome")
	}
}
