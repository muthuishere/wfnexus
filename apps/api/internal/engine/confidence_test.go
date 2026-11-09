package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// An analysis step (no action tools) never pauses on low confidence: it
// records the score and the run goes on.
func TestLowConfidenceAnalysisStepContinues(t *testing.T) {
	def := &workflow.Definition{Name: "unsure-read", Steps: []workflow.Step{{
		ID: "look", Prompt: "look", Tools: []string{"read", "grep", "glob", "bash"}, // reqsume-sentinel explain: bash is not an action
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t,
		submit(map[string]any{"ok": true, "confidence": 0.2, "why": "thin evidence"}),
		turn{text: "done"},
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	h.wait(run.ID)
	if st := h.steps(run.ID)["look"]; st.Status != "done" || !strings.Contains(string(st.Output), "0.2") {
		t.Fatalf("step %s %s", st.Status, st.Output)
	}
}

// An agent output below min_confidence waits for approval with its output
// kept; approving accepts that output without re-running the agent.
func TestLowConfidenceWaitsForApprovalThenAccepts(t *testing.T) {
	def := &workflow.Definition{Name: "unsure", Steps: []workflow.Step{{
		ID: "guess", Prompt: "guess", Tools: []string{"write"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t,
		submit(map[string]any{"ok": true, "confidence": 0.3, "why": "only one log line"}),
		turn{text: "done"},
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	h.wait(run.ID)
	st := h.steps(run.ID)["guess"]
	if st.Status != "awaiting_approval" || !strings.Contains(st.Error, "low confidence 0.30 < 0.60: only one log line") {
		t.Fatalf("step %s (%s)", st.Status, st.Error)
	}
	if err := h.eng.Approve(context.Background(), run.ID, "guess", Actor{ID: "grace", Via: "api"}); err != nil {
		t.Fatal(err)
	}
	h.wait(run.ID)
	if st := h.steps(run.ID)["guess"]; st.Status != "done" || !strings.Contains(string(st.Output), `0.3`) {
		t.Fatalf("after approve: %s %s", st.Status, st.Output)
	}
}

func TestWithConfidenceDoesNotMutate(t *testing.T) {
	s := map[string]any{"type": "object", "required": []any{"a"}, "properties": map[string]any{"a": map[string]any{}}}
	w := withConfidence(s)
	if len(s["properties"].(map[string]any)) != 1 || len(w["required"].([]any)) != 3 {
		t.Fatalf("%v / %v", s, w)
	}
}
