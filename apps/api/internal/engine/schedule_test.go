package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func dagStep(id string, needs ...string) workflow.Step {
	return workflow.Step{
		ID: id, Prompt: id, Tools: []string{"bash"}, Needs: needs,
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}
}

// The throughput claim, measured: independent steps overlap, and the wave
// finishes in the time of the slowest rather than the sum.
func TestIndependentStepsRunConcurrently(t *testing.T) {
	def := &workflow.Definition{
		Name:        "fanout",
		MaxParallel: 4,
		Steps: []workflow.Step{
			dagStep("seed"),
			dagStep("a", "seed"),
			dagStep("b", "seed"),
			dagStep("c", "seed"),
			dagStep("join", "a", "b", "c"),
		},
	}
	normalizeForTest(def)

	var mu sync.Mutex
	inFlight, maxSeen := 0, 0
	llm := newFakeLLMFunc(t, func() turn {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		mu.Unlock()
		time.Sleep(120 * time.Millisecond) // long enough for overlap to be real
		mu.Lock()
		inFlight--
		mu.Unlock()
		return submit(map[string]any{"ok": true})
	})

	h := newHarness(t, def, llm, "")
	start := time.Now()
	run := h.run(nil)
	elapsed := time.Since(start)

	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	for _, id := range []string{"seed", "a", "b", "c", "join"} {
		if steps[id].Status != "done" {
			t.Fatalf("step %s = %s", id, steps[id].Status)
		}
	}
	mu.Lock()
	peak := maxSeen
	mu.Unlock()
	if peak < 2 {
		t.Fatalf("no concurrency: peak in-flight was %d", peak)
	}
	t.Logf("peak concurrent steps=%d elapsed=%s", peak, elapsed)
}

// max_parallel is a real cap, not a suggestion.
func TestMaxParallelBoundsTheWave(t *testing.T) {
	def := &workflow.Definition{
		Name: "capped", MaxParallel: 1,
		Steps: []workflow.Step{dagStep("seed"), dagStep("a", "seed"), dagStep("b", "seed")},
	}
	normalizeForTest(def)

	var mu sync.Mutex
	inFlight, maxSeen := 0, 0
	llm := newFakeLLMFunc(t, func() turn {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		mu.Unlock()
		time.Sleep(60 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return submit(map[string]any{"ok": true})
	})
	h := newHarness(t, def, llm, "")
	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxSeen > 1 {
		t.Fatalf("max_parallel is 1 but %d steps ran at once", maxSeen)
	}
}

// A dependency is a real ordering constraint: a step must see its parent's
// typed output, which is only possible if it ran after it.
func TestDependenciesOrderTheGraph(t *testing.T) {
	def := &workflow.Definition{
		Name: "ordered",
		Steps: []workflow.Step{
			{
				ID: "first", Prompt: "p", Tools: []string{"bash"},
				OutputSchema: objSchema([]any{"value"}, map[string]any{"value": strProp()}),
			},
			{
				ID: "second", Prompt: "got {{ .Steps.first.value }}", Tools: []string{"bash"},
				Needs:        []string{"first"},
				OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
			},
		},
	}
	normalizeForTest(def)
	llm := newFakeLLM(t,
		submit(map[string]any{"value": "from-first"}), finish(),
		submit(map[string]any{"ok": true}), finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if p := h.steps(run.ID)["second"].Prompt; p != "got from-first" {
		t.Fatalf("the dependency's output did not reach the dependent step: %q", p)
	}
}

// A failing step stops the run and its dependents never start.
func TestAFailedStepBlocksItsDependents(t *testing.T) {
	def := &workflow.Definition{
		Name: "halt", MaxParallel: 2,
		Steps: []workflow.Step{
			{
				ID: "boom", Prompt: "p", Tools: []string{"bash"}, MaxAttempts: 1, MaxTurns: 2,
				OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
			},
			dagStep("after", "boom"),
		},
	}
	normalizeForTest(def)
	llm := newFakeLLM(t, turn{text: "I shall not submit"})
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if st := h.steps(run.ID)["after"]; st.Status != "pending" {
		t.Fatalf("a dependent of a failed step ran: %s", st.Status)
	}
}

// --- retry -----------------------------------------------------------------

// Retry re-runs the WHOLE step, which is coarser than the completion gate's
// model-level attempts.
func TestRetryReRunsTheWholeStep(t *testing.T) {
	def := &workflow.Definition{Name: "retry-flow", Steps: []workflow.Step{{
		ID: "flaky", Prompt: "p", Tools: []string{"bash"}, MaxAttempts: 1, MaxTurns: 2,
		Retry:        &workflow.Retry{MaxAttempts: 3},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)

	var mu sync.Mutex
	calls := 0
	llm := newFakeLLMFunc(t, func() turn {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n < 3 { // the first two whole-step attempts never submit
			return turn{text: "thinking about it"}
		}
		return submit(map[string]any{"ok": true})
	})
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 3 {
		t.Fatalf("the step was not retried: %d model calls", calls)
	}
}

func TestRetryGivesUpLoudly(t *testing.T) {
	def := &workflow.Definition{Name: "retry-exhaust", Steps: []workflow.Step{{
		ID: "doomed", Prompt: "p", Tools: []string{"bash"}, MaxAttempts: 1, MaxTurns: 2,
		Retry:        &workflow.Retry{MaxAttempts: 2},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLMFunc(t, func() turn { return turn{text: "never submitting"} })
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	if run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if run.Error == "" {
		t.Fatal("a retry exhaustion must say why")
	}
	if st := h.steps(run.ID)["doomed"]; st.Status != "failed" {
		t.Fatalf("step = %s", st.Status)
	}
}

var _ = context.Background
