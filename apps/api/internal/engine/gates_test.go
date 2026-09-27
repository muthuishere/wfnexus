package engine

import (
	"context"
	"strings"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// twoStep builds a definition whose first step carries the gate under test.
func gated(name string, gate workflow.Gate, extra ...workflow.Step) *workflow.Definition {
	def := &workflow.Definition{
		Name: name,
		Steps: append([]workflow.Step{{
			ID: "triage", Prompt: "triage", Tools: []string{"bash"},
			OutputSchema: objSchema([]any{"valid"}, map[string]any{
				"valid":   boolProp(),
				"missing": map[string]any{"type": "array", "items": strProp()},
			}),
			Gates: []workflow.Gate{gate},
		}}, extra...),
	}
	normalizeForTest(def)
	return def
}

func nextStep(id string) workflow.Step {
	return workflow.Step{
		ID: id, Prompt: id, Tools: []string{"bash"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}
}

// A needs_input gate pauses the run with a human-readable question, and
// ProvideInput resumes it from the same step with the answer in scope.
func TestNeedsInputGatePausesThenResumes(t *testing.T) {
	def := gated("needsinput",
		workflow.Gate{Field: "valid", Equals: false, Action: "needs_input",
			Message: "Need: {{ join .Output.missing \", \" }}"},
		nextStep("fix"))

	llm := newFakeLLM(t,
		submit(map[string]any{"valid": false, "missing": []any{"version", "logs"}}), finish(),
		// after the human answers, the step runs again and now passes
		submit(map[string]any{"valid": true, "missing": []any{}}), finish(),
		submit(map[string]any{"ok": true}), finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(map[string]any{"title": "t"})

	if run.Status != "needs_input" {
		t.Fatalf("run = %s, want needs_input", run.Status)
	}
	if !strings.Contains(run.Error, "version, logs") {
		t.Fatalf("question did not render the gate message: %q", run.Error)
	}
	if h.steps(run.ID)["fix"].Status != "pending" {
		t.Fatal("the later step must not have run while input was pending")
	}

	// the human answers
	if err := h.eng.ProvideInput(context.Background(), run.ID, map[string]any{"extra_context": "v2.1, logs attached"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "done" {
		t.Fatalf("after input run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if got := output(t, steps["triage"])["valid"]; got != true {
		t.Fatalf("re-run output not stored: %v", got)
	}
	if steps["fix"].Status != "done" {
		t.Fatalf("downstream step = %s", steps["fix"].Status)
	}
	// the answer must be in the run input for later prompts
	updated, _ := h.store.GetRun(context.Background(), run.ID)
	if !strings.Contains(string(updated.Input), "v2.1") {
		t.Fatalf("answer not merged into run input: %s", updated.Input)
	}
}

// `wfx answer` resolves a run parked by a needs_input GATE (no ask_human
// request stored): the gate's message is the question, the answer lands in
// input.answers, and the step re-runs.
func TestAnswerResolvesANeedsInputGate(t *testing.T) {
	def := gated("gateanswer",
		workflow.Gate{Field: "valid", Equals: false, Action: "needs_input", Message: "Which base branch?"},
		nextStep("fix"))
	llm := newFakeLLM(t,
		submit(map[string]any{"valid": false, "missing": []any{}}), finish(),
		submit(map[string]any{"valid": true, "missing": []any{}}), finish(),
		submit(map[string]any{"ok": true}), finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "needs_input" {
		t.Fatalf("run = %s, want needs_input", run.Status)
	}
	if err := h.eng.AnswerQuestion(context.Background(), run.ID, "triage",
		tn.Answer{Ok: true, Data: map[string]any{tn.RelayOutputKey: "use dev"}}, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatalf("answering a gate-parked step: %v", err)
	}
	run = h.wait(run.ID)
	if run.Status != "done" {
		t.Fatalf("after the answer run = %s (%s)", run.Status, run.Error)
	}
	updated, _ := h.store.GetRun(context.Background(), run.ID)
	if !strings.Contains(string(updated.Input), "Which base branch?") || !strings.Contains(string(updated.Input), "use dev") {
		t.Fatalf("question and answer not in input.answers: %s", updated.Input)
	}
}

// A run step parked by a gate reads the person's reply from extra_context —
// the bare reply, as the UI form writes it — never from `answers`, which also
// holds the gate's own message. Here the message shows a reply EXAMPLE; a step
// that parsed `answers` would read "approve S9" back as a decision.
func TestAnswerToAGateReachesTheStepAsExtraContextOnly(t *testing.T) {
	def := &workflow.Definition{Name: "gatereply", Steps: []workflow.Step{
		{
			ID: "decide", OutputSchema: workflow.RunOutputSchema(),
			Run: `printf '%s' '{{ .Input.extra_context }}' | grep -q . || exit 20; echo "reply=[{{ .Input.extra_context }}]"`,
			Gates: []workflow.Gate{{Field: "exitCode", Equals: 20, Action: "needs_input",
				Message: "Decide S1 and S2. Reply like: approve S9"}},
		},
		{ID: "after", Run: "echo went on", OutputSchema: workflow.RunOutputSchema()},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "needs_input" {
		t.Fatalf("run = %s (%q), want needs_input", run.Status, run.Error)
	}
	if err := h.eng.AnswerQuestion(context.Background(), run.ID, "decide",
		tn.Answer{Ok: true, Data: map[string]any{tn.RelayOutputKey: "approve S1"}}, Actor{ID: "alice", Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "done" {
		t.Fatalf("after the answer run = %s (%s)", run.Status, run.Error)
	}
	out := output(t, h.steps(run.ID)["decide"])
	if s, _ := out["stdout"].(string); !strings.Contains(s, "reply=[approve S1]") {
		t.Fatalf("the step must see exactly the reply, got %q", s)
	}
}

// A fail gate stops the run with the rendered reason and leaves later steps alone.
func TestFailGateStopsTheRun(t *testing.T) {
	def := gated("failgate",
		workflow.Gate{Field: "valid", Equals: false, Action: "fail", Message: "not actionable"},
		nextStep("fix"))

	llm := newFakeLLM(t, submit(map[string]any{"valid": false}), finish())
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if run.Error != "not actionable" {
		t.Fatalf("error = %q", run.Error)
	}
	steps := h.steps(run.ID)
	if steps["triage"].Status != "done" {
		t.Fatalf("the gated step itself succeeded, so it should read done, got %s", steps["triage"].Status)
	}
	if steps["fix"].Status != "pending" {
		t.Fatalf("later step ran despite a fail gate: %s", steps["fix"].Status)
	}
}

// A skip_to gate jumps forward, marking the bypassed steps skipped.
func TestSkipToGateJumpsForward(t *testing.T) {
	def := gated("skipgate",
		workflow.Gate{Field: "valid", Equals: true, Action: "skip_to", SkipTo: "publish"},
		nextStep("reproduce"), nextStep("fix"), nextStep("publish"))

	llm := newFakeLLM(t,
		submit(map[string]any{"valid": true}), finish(),
		submit(map[string]any{"ok": true}), finish(), // publish
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	for _, skipped := range []string{"reproduce", "fix"} {
		if steps[skipped].Status != "skipped" {
			t.Fatalf("step %s = %s, want skipped", skipped, steps[skipped].Status)
		}
	}
	if steps["publish"].Status != "done" {
		t.Fatalf("jump target = %s", steps["publish"].Status)
	}
}

// requires_approval halts BEFORE the step runs; the model is never called for
// it until a human approves.
func TestApprovalGateHaltsBeforeTheStepRuns(t *testing.T) {
	def := &workflow.Definition{
		Name: "approval",
		Steps: []workflow.Step{
			nextStep("draft"),
			{
				ID: "publish", Prompt: "publish it", Tools: []string{"bash"}, RequiresApproval: true,
				OutputSchema: objSchema([]any{"url"}, map[string]any{"url": strProp()}),
			},
		},
	}
	normalizeForTest(def)

	llm := newFakeLLM(t,
		submit(map[string]any{"ok": true}), finish(),
		submit(map[string]any{"url": "https://example.test/pr/1"}), finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "awaiting_approval" || run.CurrentStep != "publish" {
		t.Fatalf("run = %s at %s, want awaiting_approval at publish", run.Status, run.CurrentStep)
	}
	callsBefore := llm.calls()
	if h.steps(run.ID)["publish"].Turns != 0 {
		t.Fatal("the gated step consumed turns before approval")
	}

	if err := h.eng.Approve(context.Background(), run.ID, "publish", Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "done" {
		t.Fatalf("after approval run = %s (%s)", run.Status, run.Error)
	}
	if llm.calls() <= callsBefore {
		t.Fatal("approval did not actually run the step")
	}
	if got := output(t, h.steps(run.ID)["publish"])["url"]; got != "https://example.test/pr/1" {
		t.Fatalf("published output = %v", got)
	}
}

// Rejecting an approval cancels the run and records why; the step never runs.
func TestRejectStopsTheRun(t *testing.T) {
	def := &workflow.Definition{
		Name: "reject",
		Steps: []workflow.Step{nextStep("draft"), {
			ID: "publish", Prompt: "p", Tools: []string{"bash"}, RequiresApproval: true,
			OutputSchema: objSchema([]any{"url"}, map[string]any{"url": strProp()}),
		}},
	}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s", run.Status)
	}

	if err := h.eng.Reject(context.Background(), run.ID, "publish", "wrong base branch", Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run, _ = h.store.GetRun(context.Background(), run.ID)
	if run.Status != "cancelled" {
		t.Fatalf("run = %s, want cancelled", run.Status)
	}
	if !strings.Contains(run.Error, "wrong base branch") {
		t.Fatalf("reason not recorded: %q", run.Error)
	}
	if st := h.steps(run.ID)["publish"]; st.Status != "rejected" || st.Turns != 0 {
		t.Fatalf("publish = %s turns=%d", st.Status, st.Turns)
	}
}

// Approving a step that is not waiting is refused, rather than silently
// re-running work.
func TestApproveOnlyWorksOnAWaitingStep(t *testing.T) {
	def := &workflow.Definition{Name: "noapproval", Steps: []workflow.Step{nextStep("only")}}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s", run.Status)
	}
	err := h.eng.Approve(context.Background(), run.ID, "only", Actor{ID: "alice", Via: "test"})
	if err == nil || !strings.Contains(err.Error(), "awaiting_approval") {
		t.Fatalf("approve on a finished step returned %v", err)
	}
}

// Retry re-runs a completed step and discards everything downstream.
func TestRetryResetsTheStepAndItsSuccessors(t *testing.T) {
	def := &workflow.Definition{
		Name:  "retry",
		Steps: []workflow.Step{nextStep("one"), nextStep("two")},
	}
	normalizeForTest(def)
	llm := newFakeLLM(t,
		submit(map[string]any{"ok": true}), finish(),
		submit(map[string]any{"ok": true}), finish(),
		// the retry replays both steps
		submit(map[string]any{"ok": false}), finish(),
		submit(map[string]any{"ok": true}), finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s", run.Status)
	}

	if err := h.eng.Retry(context.Background(), run.ID, "one"); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "done" {
		t.Fatalf("after retry run = %s (%s)", run.Status, run.Error)
	}
	if got := output(t, h.steps(run.ID)["one"])["ok"]; got != false {
		t.Fatalf("retry did not overwrite the step output: %v", got)
	}
}
