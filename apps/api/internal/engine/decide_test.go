package engine

import (
	"encoding/json"
	"strings"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

// The judge tier is tested against the STATIC backend: live answers move run to
// run (the spike measured a score spread of 0.08 across 12 identical calls), so
// no test may assert a live number. Static needs no network and no credential.

func decideStep(gates []workflow.DecideGate) *workflow.Definition {
	def := &workflow.Definition{Name: "judged", Steps: []workflow.Step{
		{
			ID: "triage", Prompt: "triage it", Tools: []string{"bash"},
			Decide: &workflow.Decide{
				State: "cart totals go negative with a discount",
				Questions: map[string]workflow.Question{
					"fixability": {
						Type:         "score",
						Instructions: "how likely is an agent to fix this unaided",
						Levels: []string{
							"no chance: the report names no reproducible behaviour",
							"possible: the behaviour is clear but the cause is not localised",
							"likely: the failing call and the expected value are both stated",
						},
					},
					"is_security": {
						Type:         "noul",
						Instructions: "is this a security problem",
						True:         "the report describes data exposure or privilege escalation",
						False:        "the report describes ordinary incorrect behaviour",
					},
				},
				Gates: gates,
			},
			OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
		},
		nextStep("fix"),
	}}
	normalizeForTest(def)
	return def
}

// recordDecision builds the static corpus entry for the step above.
func recordDecision(t *testing.T, step *workflow.Step, fixability float64, security float64) tn.ClassifierOptions {
	t.Helper()
	qs, err := toQuestions(step.Decide.Questions)
	if err != nil {
		t.Fatal(err)
	}
	resp := map[string]any{
		"model":      "jev-1.13",
		"calibrated": true,
		"usage":      map[string]any{"input_tokens": 120, "output_tokens": 8},
		"answers": map[string]any{
			"fixability": map[string]any{
				"type": "score", "score": fixability, "confidence": 0.81,
				"probabilities": map[string]any{"0": 0.1, "1": 0.2, "2": 0.7},
				"legend":        map[string]any{"0": "no chance", "1": "possible", "2": "likely"},
			},
			"is_security": map[string]any{"type": "noul", "noul": security},
		},
	}
	raw, _ := json.Marshal(resp)
	return tn.ClassifierOptions{
		Style: tn.StyleStatic, Model: "jev-1.13",
		Decisions: []tn.RecordedDecision{{State: step.Decide.State, Questions: qs, Response: raw}},
	}
}

func TestDecideRecordsTheAnswersAndFeedsThePrompt(t *testing.T) {
	def := decideStep(nil)
	step := &def.Steps[0]
	llm := newFakeLLM(t,
		submit(map[string]any{"ok": true}), finish(),
		submit(map[string]any{"ok": true}), finish(),
	)
	h := newHarness(t, def, llm, "")
	h.eng.UseClassifier(recordDecision(t, step, 1.9, 0.04))

	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	st := h.steps(run.ID)["triage"]
	if len(st.Decision) == 0 {
		t.Fatal("the decision was not stored on the step")
	}
	var rec decisionRecord
	if err := json.Unmarshal(st.Decision, &rec); err != nil {
		t.Fatal(err)
	}
	if !rec.Calibrated {
		t.Fatal("calibrated should round-trip as true")
	}
	if a := rec.Answers["fixability"]; a.Score == nil || *a.Score != 1.9 {
		t.Fatalf("score answer = %+v", a)
	}
	if a := rec.Answers["is_security"]; a.Noul == nil || *a.Noul != 0.04 {
		t.Fatalf("noul answer = %+v", a)
	}
	if len(rec.Answers["fixability"].Probabilities) != 3 {
		t.Fatalf("probabilities lost: %+v", rec.Answers["fixability"])
	}
}

// The gate runs BEFORE the agent — that is the point: a cheap judgment stops
// the run instead of a full agent turn.
func TestDecideGateHaltsBeforeTheAgentRuns(t *testing.T) {
	msg := "too vague to work on"
	def := decideStep([]workflow.DecideGate{{
		Question: "fixability", Below: f(1.0), Action: "needs_input", Message: msg,
	}})
	step := &def.Steps[0]
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	h.eng.UseClassifier(recordDecision(t, step, 0.4, 0.02)) // below the threshold

	run := h.run(nil)
	if run.Status != "needs_input" {
		t.Fatalf("run = %s, want needs_input", run.Status)
	}
	if run.Error != msg {
		t.Fatalf("error = %q", run.Error)
	}
	if llm.calls() != 0 {
		t.Fatalf("the agent ran %d times despite the judge halting the step", llm.calls())
	}
	if h.steps(run.ID)["fix"].Status != "pending" {
		t.Fatal("a later step ran")
	}
}

func TestDecideGateDoesNotFireAboveTheThreshold(t *testing.T) {
	def := decideStep([]workflow.DecideGate{{
		Question: "fixability", Below: f(1.0), Action: "fail", Message: "too vague",
	}})
	step := &def.Steps[0]
	llm := newFakeLLM(t,
		submit(map[string]any{"ok": true}), finish(),
		submit(map[string]any{"ok": true}), finish(),
	)
	h := newHarness(t, def, llm, "")
	h.eng.UseClassifier(recordDecision(t, step, 1.9, 0.02))

	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if llm.calls() == 0 {
		t.Fatal("the agent never ran although the judge cleared it")
	}
}

func TestDecideNoulGateOnAtLeast(t *testing.T) {
	def := decideStep([]workflow.DecideGate{{
		Question: "is_security", AtLeast: f(0.5), Action: "fail", Message: "security reports go to the security queue",
	}})
	step := &def.Steps[0]
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	h.eng.UseClassifier(recordDecision(t, step, 2.0, 0.93)) // high security signal

	run := h.run(nil)
	if run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if !strings.Contains(run.Error, "security queue") {
		t.Fatalf("error = %q", run.Error)
	}
}

// A backend that cannot answer must error, never guess — the static backend
// refuses an unrecorded state rather than returning a neighbour's answer.
func TestDecideFailsLoudlyOnAnUnrecordedState(t *testing.T) {
	def := decideStep(nil)
	step := &def.Steps[0]
	opts := recordDecision(t, step, 1.9, 0.02)
	opts.Decisions[0].State = "a completely different bug report"

	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	h.eng.UseClassifier(opts)

	run := h.run(nil)
	if run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if !strings.Contains(run.Error, "classifier") {
		t.Fatalf("error should name the classifier: %q", run.Error)
	}
	if llm.calls() != 0 {
		t.Fatal("the agent ran despite the judge failing")
	}
}

func TestDecideGateConditions(t *testing.T) {
	vals := map[string]any{"score": 1.4, "pick": "billing", "truth": 0.9}
	cases := []struct {
		name string
		gate workflow.DecideGate
		want bool
	}{
		{"below hit", workflow.DecideGate{Question: "score", Below: f(2.0)}, true},
		{"below miss", workflow.DecideGate{Question: "score", Below: f(1.0)}, false},
		{"at_least hit", workflow.DecideGate{Question: "truth", AtLeast: f(0.5)}, true},
		{"at_least boundary is inclusive", workflow.DecideGate{Question: "truth", AtLeast: f(0.9)}, true},
		{"is hit", workflow.DecideGate{Question: "pick", Is: "billing"}, true},
		{"is miss", workflow.DecideGate{Question: "pick", Is: "shipping"}, false},
		{"unknown question never fires", workflow.DecideGate{Question: "ghost", Below: f(99)}, false},
		{"type mismatch never fires", workflow.DecideGate{Question: "pick", Below: f(99)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decideGate(c.gate, vals); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func f(v float64) *float64 { return &v }
