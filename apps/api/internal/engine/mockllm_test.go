package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/registry"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The mock's whole job: produce a value that SATISFIES the declared schema. A
// stub returning `{}` would fail the contract, the step would fail, and the mock
// would only prove the mock was broken.
func TestMockValueSatisfiesTheDeclaredShape(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"ok", "count", "title", "tags", "nested"},
		"properties": map[string]any{
			"ok":    map[string]any{"type": "boolean"},
			"count": map[string]any{"type": "integer", "minimum": float64(3)},
			"title": map[string]any{"type": "string", "minLength": float64(12)},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": float64(2)},
			"verdict": map[string]any{
				"type": "string", "enum": []any{"pass", "fail"},
			},
			"nested": map[string]any{
				"type":     "object",
				"required": []any{"inner"},
				"properties": map[string]any{
					"inner": map[string]any{"type": "number", "maximum": float64(0)},
				},
			},
		},
	}
	got, ok := mockValue(schema, 0).(map[string]any)
	if !ok {
		t.Fatalf("not an object: %#v", got)
	}
	for _, name := range []string{"ok", "count", "title", "tags", "nested", "verdict"} {
		if _, present := got[name]; !present {
			t.Errorf("%s is missing; a required field absent fails the contract", name)
		}
	}
	if got["ok"] != true {
		t.Errorf("ok = %#v", got["ok"])
	}
	if n, _ := mockNumber(got["count"]); n < 3 {
		t.Errorf("count = %#v, below the declared minimum", got["count"])
	}
	if s, _ := got["title"].(string); len(s) < 12 {
		t.Errorf("title = %q, below minLength", s)
	}
	if arr, _ := got["tags"].([]any); len(arr) < 2 {
		t.Errorf("tags = %#v, below minItems", got["tags"])
	}
	// An enum takes the FIRST value, always: a random pick would make two runs
	// disagree, and reproducibility is the reason to run a mock at all.
	if got["verdict"] != "pass" {
		t.Errorf("verdict = %#v, want the first enum value", got["verdict"])
	}
	if inner, _ := got["nested"].(map[string]any); inner != nil {
		if n, _ := mockNumber(inner["inner"]); n > 0 {
			t.Errorf("inner = %#v, above the declared maximum", inner["inner"])
		}
	}

	// Nothing it produces may read as a real answer. That is the difference
	// between a useful mock and a demo that lies.
	raw, _ := json.Marshal(got)
	for _, s := range []string{"mock"} {
		if !strings.Contains(string(raw), s) {
			t.Errorf("no value says %q, so a mock result could be mistaken for a real one: %s", s, raw)
		}
	}
}

// Deterministic: the same schema twice is the same value. Go randomises map
// order, so this is a real risk and not a theoretical one.
func TestMockValueIsDeterministic(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"},
		"c": map[string]any{"type": "string"}, "d": map[string]any{"type": "string"},
	}}
	first, _ := json.Marshal(mockValue(schema, 0))
	for i := 0; i < 20; i++ {
		again, _ := json.Marshal(mockValue(schema, 0))
		if string(again) != string(first) {
			t.Fatalf("run %d differed:\n%s\n%s", i, first, again)
		}
	}
}

// A schema that refers to itself must not build a value forever.
func TestMockValueStopsOnADeepSchema(t *testing.T) {
	deep := map[string]any{"type": "object", "properties": map[string]any{}}
	cur := deep
	for i := 0; i < 50; i++ {
		next := map[string]any{"type": "object", "properties": map[string]any{}}
		cur["properties"].(map[string]any)["down"] = next
		cur = next
	}
	done := make(chan any, 1)
	go func() { done <- mockValue(deep, 0) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mockValue did not stop on a deep schema")
	}
}

// A WHOLE WORKFLOW RUNS. This is the point of the provider: somebody who has
// configured nothing can see gates fire, facts flow and the typed contract hold.
func TestAWorkflowRunsEndToEndOnTheMockProvider(t *testing.T) {
	def := &workflow.Definition{
		Name: "mocked",
		Steps: []workflow.Step{
			{
				ID: "survey", Provider: "mock", Prompt: "look at it",
				OutputSchema: objSchema([]any{"finding"}, map[string]any{"finding": strProp()}),
			},
			{
				ID: "act", Provider: "mock", Prompt: "act on {{.Steps.survey.finding}}",
				Needs:        []string{"survey"},
				OutputSchema: objSchema([]any{"done"}, map[string]any{"done": map[string]any{"type": "boolean"}}),
			},
		},
	}
	normalizeForTest(def)
	st, bl, cfg := testDeps(t)
	cfg.WorkDir = t.TempDir()
	eng := New(cfg, st, bl, map[string]*workflow.Definition{def.Name: def},
		skills.Load(t.TempDir()), mockCatalog())
	h := &harness{eng: eng, store: st, t: t}
	t.Cleanup(func() {
		for _, id := range h.runs {
			eng.Cancel(context.Background(), id)
		}
		time.Sleep(50 * time.Millisecond)
	})
	run := h.run(nil)

	if run.Status != "done" {
		t.Logf("run error: %v", run.Error)
		for id, st := range h.steps(run.ID) {
			t.Logf("  step %s = %s: %v", id, st.Status, st.Error)
		}
		t.Fatalf("run = %s, want done — a mock run must complete or it proves nothing", run.Status)
	}
	steps := h.steps(run.ID)
	for _, id := range []string{"survey", "act"} {
		st, ok := steps[id]
		if !ok {
			t.Fatalf("%s did not run", id)
		}
		if st.Status != "done" {
			t.Errorf("%s = %s; the mock's output did not satisfy the step's own schema", id, st.Status)
		}
	}
}

func mockCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Providers:   registry.New[catalog.Provider]("provider", catalog.Provider{Name: "mock", Kind: catalog.KindMock}),
		Classifiers: registry.New[catalog.Classifier]("classifier"),
		Mcp:         registry.New[catalog.McpServer]("mcp server"),
		Notifiers:   registry.New[catalog.Notifier]("notifier"),
	}
}

// The mock must submit ONCE. The first version decided "already submitted" by
// looking for "submit_output" in the tool result, and the native tool returns
// "accepted" (step.go) — so the check never matched, it resubmitted every turn,
// and every step ran to the turn cap: 30 model calls instead of 1. Free, and a
// complete lie about what a run costs in turns.
//
// The signal is the handler's OWN previous tool call. Matching the other side's
// wording was a guess about somebody else's string.
func TestTheMockSubmitsOnceAndThenCloses(t *testing.T) {
	def := &workflow.Definition{
		Name: "once",
		Steps: []workflow.Step{{
			ID: "only", Provider: "mock", Prompt: "do it",
			OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": map[string]any{"type": "boolean"}}),
		}},
	}
	normalizeForTest(def)
	st, bl, cfg := testDeps(t)
	cfg.WorkDir = t.TempDir()
	eng := New(cfg, st, bl, map[string]*workflow.Definition{def.Name: def},
		skills.Load(t.TempDir()), mockCatalog())
	h := &harness{eng: eng, store: st, t: t}
	t.Cleanup(func() {
		for _, id := range h.runs {
			eng.Cancel(context.Background(), id)
		}
		time.Sleep(50 * time.Millisecond)
	})

	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s", run.Status)
	}
	step := h.steps(run.ID)["only"]
	// Two turns is the honest shape: submit, then the wrap-up reply that closes
	// the step. Anything near the cap means the mock is arguing with itself.
	if step.Turns > 3 {
		t.Errorf("the step took %d turns; the mock resubmitted instead of closing", step.Turns)
	}
}
