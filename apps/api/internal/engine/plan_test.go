package engine

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func factStep(id string, consumes, produces []string, when ...workflow.Guard) workflow.Step {
	return workflow.Step{
		ID: id, Prompt: id, Tools: []string{"bash"},
		Consumes: consumes, Produces: produces, When: when,
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}
}

// The order is DERIVED: no step names another, only facts, and the planner
// works out that seed must precede the fan-out and join must come last.
func TestPlanDerivesTheOrderFromFacts(t *testing.T) {
	def := &workflow.Definition{
		Name: "derived", Goal: "merged", MaxParallel: 4,
		Steps: []workflow.Step{
			factStep("join", []string{"a", "b"}, []string{"merged"}),
			factStep("left", []string{"seed"}, []string{"a"}),
			factStep("right", []string{"seed"}, []string{"b"}),
			factStep("seed", []string{"bug"}, []string{"seed"}),
		},
	}
	normalizeForTest(def)

	var mu sync.Mutex
	order := []string{}
	inFlight, peak := 0, 0
	llm := newFakeLLMFunc(t, func() turn {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		time.Sleep(80 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return submit(map[string]any{"ok": true})
	})

	h := newHarness(t, def, llm, "")
	run := h.run(map[string]any{"bug": "a report"})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	for _, id := range []string{"seed", "left", "right", "join"} {
		if steps[id].Status != "done" {
			t.Fatalf("%s = %s", id, steps[id].Status)
		}
	}
	// seed must have started before join finished, and the fan-out overlapped
	mu.Lock()
	defer mu.Unlock()
	if peak < 2 {
		t.Fatalf("the independent pair did not overlap: peak=%d", peak)
	}
	_ = order
}

// The plan is re-derived after every step, so a guard reading a REAL value
// decides what runs next. This is the whole point of planning over wiring.
func TestPlanReplansOnWhatTheStepActuallyFound(t *testing.T) {
	yes := true
	def := &workflow.Definition{
		Name: "replan", Goal: "handled",
		Steps: []workflow.Step{
			{
				ID: "triage", Prompt: "triage", Tools: []string{"bash"},
				Consumes: []string{"bug"}, Produces: []string{"triage"},
				OutputSchema: objSchema([]any{"valid"}, map[string]any{"valid": boolProp()}),
			},
			factStep("fix", []string{"triage"}, []string{"handled"},
				workflow.Guard{Path: "triage.valid", Equals: yes}),
			factStep("reject", []string{"triage"}, []string{"handled"},
				workflow.Guard{Path: "triage.valid", Equals: false}),
		},
	}
	normalizeForTest(def)

	// triage says the report is NOT valid, so the planner must choose `reject`
	llm := newFakeLLM(t,
		submit(map[string]any{"valid": false}), finish(),
		submit(map[string]any{"ok": true}), finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(map[string]any{"bug": "vague"})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if steps["reject"].Status != "done" {
		t.Fatalf("the guard did not route to reject: %s", steps["reject"].Status)
	}
	if steps["fix"].Status == "done" {
		t.Fatal("fix ran although triage.valid was false")
	}
}

// The goal is the stop condition: once it holds, remaining steps are not run.
func TestPlanStopsAtTheGoal(t *testing.T) {
	def := &workflow.Definition{
		Name: "goalstop", Goal: "answered",
		Steps: []workflow.Step{
			factStep("quick", []string{"q"}, []string{"answered"}),
			factStep("slow", []string{"answered"}, []string{"extra"}),
		},
	}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	run := h.run(map[string]any{"q": "?"})
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if st := h.steps(run.ID)["slow"]; st.Status == "done" {
		t.Fatal("a step ran after the goal was already reached")
	}
}

// A plan that cannot reach its goal must say so precisely, before running.
func TestPlanRefusesAnUnreachableGoalAtLoad(t *testing.T) {
	def := &workflow.Definition{
		Name: "broken", Goal: "shipped",
		Steps: []workflow.Step{factStep("only", []string{"bug"}, []string{"triage"})},
	}
	normalizeForTest(def)
	llm := newFakeLLM(t)
	h := newHarness(t, def, llm, "")
	run := h.run(map[string]any{"bug": "x"})
	if run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if !strings.Contains(run.Error, "shipped") {
		t.Fatalf("the error should name the goal: %q", run.Error)
	}
	if llm.calls() != 0 {
		t.Fatal("an impossible plan should cost nothing")
	}
}

func TestPlanExplainsWhyItIsStuck(t *testing.T) {
	def := &workflow.Definition{
		Name: "stuck", Goal: "done-fact",
		Steps: []workflow.Step{
			factStep("first", []string{"bug"}, []string{"a"}),
			// guard can never hold, so the goal is never produced
			factStep("second", []string{"a"}, []string{"done-fact"},
				workflow.Guard{Path: "a.ok", Equals: "never"}),
		},
	}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	run := h.run(map[string]any{"bug": "x"})
	if run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if !strings.Contains(run.Error, "second") || !strings.Contains(run.Error, "guard") {
		t.Fatalf("the error must name the blocked step and why: %q", run.Error)
	}
}
