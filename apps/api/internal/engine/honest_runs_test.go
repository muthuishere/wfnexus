package engine

import (
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// prepare-worker exited 1 and the run said "done". A non-zero exit the
// workflow did not opt out of fails the step AND the run, and keeps the
// output so the failure can be read.
func TestRunStepNonZeroExitFailsTheRun(t *testing.T) {
	def := &workflow.Definition{Name: "redsuite", Steps: []workflow.Step{
		{ID: "tests", Run: "echo boom >&2; exit 3", OutputSchema: workflow.RunOutputSchema()},
		{ID: "after", Run: "echo never", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)

	if run.Status != "failed" || !strings.Contains(run.Error, "exited 3") || !strings.Contains(run.Error, "boom") {
		t.Fatalf("run = %s (%q), want failed naming exit 3 and stderr", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if steps["tests"].Status != "failed" {
		t.Fatalf("step = %s", steps["tests"].Status)
	}
	if got, _ := output(t, steps["tests"])["exitCode"].(float64); got != 3 {
		t.Fatalf("output not kept: %v", output(t, steps["tests"]))
	}
	if steps["after"].Status == "done" {
		t.Fatal("a step after a failed command ran")
	}
}

// The same on the DAG path, which is what prepare-worker uses.
func TestRunStepNonZeroExitFailsADAGRun(t *testing.T) {
	def := &workflow.Definition{Name: "dag", Steps: []workflow.Step{
		{ID: "a", Run: "exit 1", OutputSchema: workflow.RunOutputSchema()},
		{ID: "b", Needs: []string{"a"}, Run: "echo never", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "failed" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if st := h.steps(run.ID)["b"]; st.Status == "done" {
		t.Fatal("a dependent of a failed command ran")
	}
}

// continue-on-error is the explicit opt-out: the exit is an answer a later
// step routes on (workflow-doctor's verify: 4 = nothing to verify).
func TestContinueOnErrorKeepsTheExitAsAnAnswer(t *testing.T) {
	def := &workflow.Definition{Name: "answer", Steps: []workflow.Step{
		{ID: "verify", Run: "exit 4", ContinueOnError: true, OutputSchema: workflow.RunOutputSchema()},
		{ID: "review", Needs: []string{"verify"}, Run: "echo reviewed", OutputSchema: workflow.RunOutputSchema(),
			When: []workflow.Guard{{Path: "verify.exitCode", Equals: 0}}},
		{ID: "report", Needs: []string{"verify"}, Run: "echo reported", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if steps["verify"].Status != "done" || steps["report"].Status != "done" {
		t.Fatalf("verify=%s report=%s", steps["verify"].Status, steps["report"].Status)
	}
	if steps["review"].Status != "skipped" {
		t.Fatalf("the branch not taken must be skipped, got %q", steps["review"].Status)
	}
	if got, want := steps["review"].SkipReason, "when verify.exitCode = 0 (was 4)"; got != want {
		t.Fatalf("skip reason %q, want %q", got, want)
	}
}

// A gate that matches the output is the workflow saying what the exit means,
// so the step routes on it instead of failing generically.
func TestAMatchingGateHandlesTheExit(t *testing.T) {
	def := &workflow.Definition{Name: "skipto", Steps: []workflow.Step{
		{ID: "check", Run: "exit 78", OutputSchema: workflow.RunOutputSchema(),
			Gates: []workflow.Gate{{Field: "exitCode", Equals: 78, Action: "skip_to", SkipTo: "end"}}},
		{ID: "middle", Run: "echo never", OutputSchema: workflow.RunOutputSchema()},
		{ID: "end", Run: "echo end", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if steps["middle"].Status != "skipped" || steps["end"].Status != "done" {
		t.Fatalf("middle=%s end=%s", steps["middle"].Status, steps["end"].Status)
	}
	if got := steps["middle"].SkipReason; got != "jumped over: check's gate on exitCode skipped to end" {
		t.Fatalf("skip reason %q", got)
	}
}

// A gate on a DIFFERENT exit does not excuse this one.
func TestANonMatchingGateDoesNotExcuseTheExit(t *testing.T) {
	def := &workflow.Definition{Name: "other", Steps: []workflow.Step{
		{ID: "check", Run: "exit 1", OutputSchema: workflow.RunOutputSchema(),
			Gates: []workflow.Gate{{Field: "exitCode", Equals: 64, Action: "fail", Message: "no module"}}},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "failed" || !strings.Contains(run.Error, "exited 1") {
		t.Fatalf("run = %s (%q)", run.Status, run.Error)
	}
}

// A failing exit is retried under the step's retry policy, like any failure.
func TestAFailingExitIsRetried(t *testing.T) {
	ws := t.TempDir()
	def := &workflow.Definition{Name: "flaky", Steps: []workflow.Step{
		{ID: "flaky", Run: "if [ -f n ]; then exit 0; fi; touch n; exit 1",
			Retry: &workflow.Retry{MaxAttempts: 2}, OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(map[string]any{"repo_path": ws, "isolate": false})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
}

// Sequential path: a guard that routes around a step leaves it skipped, and
// nothing on a finished run is left pending.
func TestNothingIsLeftPendingOnAFinishedRun(t *testing.T) {
	def := &workflow.Definition{Name: "seq", Steps: []workflow.Step{
		{ID: "a", Run: "echo a", OutputSchema: workflow.RunOutputSchema()},
		{ID: "b", Run: "echo b", OutputSchema: workflow.RunOutputSchema(),
			When: []workflow.Guard{{Path: "a.exitCode", Equals: 9}}},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	for id, st := range h.steps(run.ID) {
		if st.Status == "pending" {
			t.Fatalf("step %s left pending on a finished run", id)
		}
	}
}
