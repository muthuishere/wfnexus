package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// THE RUN TELLS ITS STORY: a `run:` step writes a summary, an outcome and
// state through files named in its environment, and the engine keeps them.

func TestStepSummaryOutcomeAndState(t *testing.T) {
	name := "story-" + uuid.NewString()[:8]
	h := newHarness(t, &workflow.Definition{
		Name: name,
		Steps: []workflow.Step{
			{ID: "look", Run: `printf '## Checked 3 services\nall healthy\n' >> "$WFX_STEP_SUMMARY"; echo '{"last_seen":"16:15","count":3}' > "$WFX_STATE"`},
			{ID: "decide", Run: `echo 'incident: api 5xx at 4%' > "$WFX_OUTCOME"`},
		},
	}, newFakeLLM(t), "")
	t.Cleanup(func() {
		ctx := context.Background()
		_ = h.eng.DeleteState(ctx, model.StateScopeWorkflow, name, "last_seen")
		_ = h.eng.DeleteState(ctx, model.StateScopeWorkflow, name, "count")
	})
	ctx := context.Background()
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run: %s — %s", run.Status, run.Error)
	}
	if run.Outcome != "incident: api 5xx at 4%" {
		t.Fatalf("outcome = %q", run.Outcome)
	}
	steps, err := h.store.ListSteps(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var look map[string]any
	for _, s := range steps {
		if s.StepID == "look" {
			_ = json.Unmarshal(s.Output, &look)
		}
	}
	if sm, _ := look["summary"].(string); !strings.Contains(sm, "Checked 3 services") {
		t.Fatalf("summary not kept: %v", look)
	}
	if v, _, _ := h.eng.GetState(ctx, model.StateScopeWorkflow, name, "last_seen"); v != "16:15" {
		t.Fatalf("state last_seen = %q", v)
	}
	if v, _, _ := h.eng.GetState(ctx, model.StateScopeWorkflow, name, "count"); v != "3" {
		t.Fatalf("state count = %q", v)
	}
}

// No outcome written: the headline is the first line of the last summary.
func TestOutcomeDerivedFromSummary(t *testing.T) {
	h := newHarness(t, &workflow.Definition{
		Name:  "story-derive-" + uuid.NewString()[:8],
		Steps: []workflow.Step{{ID: "s", Run: `echo '# all clear' >> "$WFX_STEP_SUMMARY"`}},
	}, newFakeLLM(t), "")
	if run := h.run(nil); run.Outcome != "ok: all clear" {
		t.Fatalf("outcome = %q (status %s)", run.Outcome, run.Status)
	}
}

func TestNormalizeOutcome(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		"ok: all clear\n":         "ok: all clear",
		"INCIDENT:  db down":      "incident: db down",
		"waiting":                 "waiting",
		"nothing to do":           "ok: nothing to do",
		"first\nfailed: boom\n\n": "failed: boom",
	} {
		if got := NormalizeOutcome(in); got != want {
			t.Errorf("NormalizeOutcome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveOutcome(t *testing.T) {
	out := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	steps := []*model.StepRun{
		{StepID: "b", Position: 1, Output: out(map[string]any{"summary": "### 2 alerts triaged\nmore"})},
		{StepID: "a", Position: 0, Output: out(map[string]any{"summary": "early"})},
	}
	if got := DeriveOutcome("done", "", steps); got != "ok: 2 alerts triaged" {
		t.Fatalf("done: %q", got)
	}
	if got := DeriveOutcome("failed", "command exited 1\nstack", steps); got != "failed: command exited 1" {
		t.Fatalf("failed: %q", got)
	}
	steps = append(steps, &model.StepRun{Position: 2, Output: out(map[string]any{"outcome": "incident: x"})})
	if got := DeriveOutcome("failed", "boom", steps); got != "incident: x" {
		t.Fatalf("failed with incident: %q", got)
	}
	if got := DeriveOutcome("done", "", nil); got != "ok" {
		t.Fatalf("empty: %q", got)
	}
}
