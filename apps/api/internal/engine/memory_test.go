package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// WORKFLOW MEMORY, end to end against the real engine and a scripted model:
// what one run leaves behind is what the next run's agent reads.

var verdictSchema = map[string]any{
	"type":     "object",
	"required": []any{"verdict"},
	"properties": map[string]any{
		"verdict": map[string]any{"type": "string"},
	},
}

func memName(prefix string) string { return prefix + "-" + uuid.NewString()[:8] }

// runNamed starts a run of a named workflow and waits for it.
func (h *harness) runNamed(name string, input map[string]any) *store.Run {
	h.t.Helper()
	raw, _ := json.Marshal(input)
	run, err := h.store.CreateRun(context.Background(), "local", name, raw)
	if err != nil {
		h.t.Fatal(err)
	}
	h.runs = append(h.runs, run.ID)
	h.eng.Start(run.ID)
	return h.wait(run.ID)
}

func forgetAfter(t *testing.T, h *harness, names ...string) {
	t.Cleanup(func() {
		for _, n := range names {
			_, _ = h.eng.ForgetMemory(context.Background(), n, "")
		}
	})
}

func TestMemoryRunTwoSeesRunOne(t *testing.T) {
	name := memName("mem")
	llm := newFakeLLM(t,
		// run 1: record a lesson, then submit
		callTool("remember", map[string]any{"lesson": "the cache lives in /var/cache/app"}),
		submit(map[string]any{"verdict": "first-verdict-xyz"}), finish(),
		// run 2
		submit(map[string]any{"verdict": "second"}), finish(),
	)
	h := newHarness(t, &workflow.Definition{
		Name:  name,
		Steps: []workflow.Step{{ID: "triage", Prompt: "Triage {{ .Input.ticket }}", OutputSchema: verdictSchema, MaxTurns: 6, MaxAttempts: 1}},
	}, llm, "")
	forgetAfter(t, h, name)

	if run := h.run(map[string]any{"ticket": "T-1"}); run.Status != "done" {
		t.Fatalf("run 1: %s — %s", run.Status, run.Error)
	}
	if p := llm.promptOf(0); strings.Contains(p, memoryHeading) {
		t.Fatalf("the first run had memory before anything ran:\n%s", p)
	}
	offered := strings.Join(llm.toolNamesOffered(0), ",")
	if !strings.Contains(offered, "remember") {
		t.Fatalf("remember not offered by default: %s", offered)
	}

	if run := h.run(map[string]any{"ticket": "T-2"}); run.Status != "done" {
		t.Fatalf("run 2: %s — %s", run.Status, run.Error)
	}
	first2 := 3 // requests 0..2 were run 1 (remember, submit, finish)
	p := llm.promptOf(first2)
	t.Logf("run 2 prompt:\n%s", p)
	for _, want := range []string{memoryHeading, "first-verdict-xyz", "T-1", "the cache lives in /var/cache/app", "[triage]"} {
		if !strings.Contains(p, want) {
			t.Fatalf("run 2's prompt lacks %q:\n%s", want, p)
		}
	}
	mem, err := h.eng.WorkflowMemory(context.Background(), name, "local")
	if err != nil {
		t.Fatal(err)
	}
	runs, notes := 0, 0
	for _, m := range mem {
		switch m.Kind {
		case model.MemoryKindRun:
			runs++
		case model.MemoryKindNote:
			notes++
		}
	}
	if runs != 2 || notes != 1 {
		t.Fatalf("after two runs: %d run records, %d notes", runs, notes)
	}
}

func TestMemoryOptOut(t *testing.T) {
	name := memName("mem-off")
	llm := newFakeLLM(t,
		submit(map[string]any{"verdict": "a"}), finish(),
		submit(map[string]any{"verdict": "b"}), finish(),
	)
	h := newHarness(t, &workflow.Definition{
		Name:   name,
		Memory: &workflow.Memory{Off: true},
		Steps:  []workflow.Step{{ID: "s", Prompt: "go", OutputSchema: verdictSchema, MaxTurns: 6, MaxAttempts: 1}},
	}, llm, "")
	forgetAfter(t, h, name)
	for i := 0; i < 2; i++ {
		if run := h.run(nil); run.Status != "done" {
			t.Fatalf("run %d: %s — %s", i, run.Status, run.Error)
		}
	}
	if p := llm.promptOf(2); strings.Contains(p, memoryHeading) {
		t.Fatal("memory: false still loaded memory")
	}
	if strings.Contains(strings.Join(llm.toolNamesOffered(0), ","), "remember") {
		t.Fatal("memory: false still offered remember")
	}
	if mem, _ := h.eng.WorkflowMemory(context.Background(), name, ""); len(mem) != 0 {
		t.Fatalf("memory: false still recorded %d entries", len(mem))
	}
}

func TestMemoryIncludesAnotherWorkflow(t *testing.T) {
	a, b := memName("mem-a"), memName("mem-b")
	llm := newFakeLLM(t, submit(map[string]any{"verdict": "b-ok"}), finish())
	h := newHarness(t, &workflow.Definition{
		Name: a,
		Steps: []workflow.Step{{ID: "probe", Run: `printf %s sibling-fact-42`,
			OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"stdout": map[string]any{"type": "string"}}}}},
	}, llm, "")
	h.eng.defs[b] = &workflow.Definition{
		Name:   b,
		Memory: &workflow.Memory{Include: []string{"workflow:" + a}},
		Steps:  []workflow.Step{{ID: "use", Prompt: "Use what {{ .Input.x }} knows", OutputSchema: verdictSchema, MaxTurns: 6, MaxAttempts: 1}},
	}
	forgetAfter(t, h, a, b)

	if run := h.runNamed(a, nil); run.Status != "done" {
		t.Fatalf("A: %s — %s", run.Status, run.Error)
	}
	if run := h.runNamed(b, map[string]any{"x": "A"}); run.Status != "done" {
		t.Fatalf("B: %s — %s", run.Status, run.Error)
	}
	p := llm.promptOf(0)
	if !strings.Contains(p, "### From workflow `"+a+"`") || !strings.Contains(p, "sibling-fact-42") {
		t.Fatalf("B did not see A's memory:\n%s", p)
	}

	// Without the include, B sees nothing of A.
	h.eng.defs[b].Memory = nil
	if got := h.eng.memoryFor(context.Background(), h.eng.defs[b], "use", "local"); strings.Contains(got, "sibling-fact-42") {
		t.Fatal("another workflow's memory loaded without an include")
	}
}

func TestMemoryBudgetTruncates(t *testing.T) {
	name := memName("mem-budget")
	h := newHarness(t, &workflow.Definition{Name: name, Steps: []workflow.Step{{ID: "s", Run: "true"}}}, newFakeLLM(t), "")
	forgetAfter(t, h, name)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := h.store.AddMemory(ctx, model.MemoryEntry{
			Project: "local", Workflow: name, Kind: model.MemoryKindRun, RunID: uuid.NewString(),
			Body: strings.Repeat("x", 400), CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}
	plan := workflow.MemoryPlan{On: true, Budget: 100} // 400 characters
	got := h.eng.memorySection(ctx, name, "local", false, plan)
	if len(got) > 100*memoryCharsPerToken {
		t.Fatalf("section is %d characters, budget is %d", len(got), 100*memoryCharsPerToken)
	}
	if !strings.Contains(got, "truncated to its budget") || !strings.HasPrefix(got, memoryHeading) {
		t.Fatalf("truncation is not visible:\n%s", got)
	}
	// The default budget holds all five.
	full := h.eng.memorySection(ctx, name, "local", false, workflow.EffectiveMemory(&workflow.Definition{}, nil))
	if strings.Contains(full, "truncated") {
		t.Fatal("default budget truncated 2KB of memory")
	}
}

func TestMemoryNeverStoresASecret(t *testing.T) {
	name := memName("mem-secret")
	const secret = "ghp_memorysecretvalue12345"
	h := newHarness(t, &workflow.Definition{
		Name: name,
		Steps: []workflow.Step{{ID: "leak", Run: `printf %s "$GH_TOKEN"`,
			Env:          map[string]string{"GH_TOKEN": secret},
			OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"stdout": map[string]any{"type": "string"}}}}},
	}, newFakeLLM(t), "")
	forgetAfter(t, h, name)
	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("run: %s — %s", run.Status, run.Error)
	}
	mem, _ := h.eng.WorkflowMemory(context.Background(), name, "")
	if len(mem) != 1 {
		t.Fatalf("want one record, got %d", len(mem))
	}
	if strings.Contains(mem[0].Body, secret) || strings.Contains(mem[0].Body, secret[:20]) {
		t.Fatalf("a secret reached memory:\n%s", mem[0].Body)
	}
	if !strings.Contains(mem[0].Body, "[redacted:GH_TOKEN]") {
		t.Fatalf("expected the redaction marker:\n%s", mem[0].Body)
	}
}

func TestMemoryFailedRunAndRetention(t *testing.T) {
	name := memName("mem-retain")
	h := newHarness(t, &workflow.Definition{
		Name:   name,
		Memory: &workflow.Memory{Retain: 2},
		Steps:  []workflow.Step{{ID: "boom", Run: `echo nope-{{ .Input.n }} >&2; exit 3`}},
	}, newFakeLLM(t), "")
	forgetAfter(t, h, name)
	for i := 1; i <= 3; i++ {
		if run := h.run(map[string]any{"n": i}); run.Status != "failed" {
			t.Fatalf("run %d: %s", i, run.Status)
		}
	}
	mem, _ := h.eng.WorkflowMemory(context.Background(), name, "local")
	var runs, digests []model.MemoryEntry
	for _, m := range mem {
		switch m.Kind {
		case model.MemoryKindRun:
			runs = append(runs, m)
		case model.MemoryKindDigest:
			digests = append(digests, m)
		}
	}
	if len(runs) != 2 || len(digests) != 1 {
		t.Fatalf("retain 2 after 3 runs: %d records, %d digests", len(runs), len(digests))
	}
	if !strings.Contains(runs[0].Body, "boom: failed") || !strings.Contains(runs[0].Body, "exited 3") {
		t.Fatalf("a failed run's record does not say what failed:\n%s", runs[0].Body)
	}
	if !strings.Contains(digests[0].Body, "failed") {
		t.Fatalf("digest lost the folded run:\n%s", digests[0].Body)
	}
}

func TestDryRunShowsMemory(t *testing.T) {
	name := memName("mem-dry")
	h := newHarness(t, &workflow.Definition{Name: name, Steps: []workflow.Step{{ID: "s", Prompt: "go", OutputSchema: verdictSchema, MaxTurns: 6, MaxAttempts: 1}}}, newFakeLLM(t), "")
	forgetAfter(t, h, name)
	if _, err := h.store.AddMemory(context.Background(), model.MemoryEntry{
		Project: "local", Workflow: name, Kind: model.MemoryKindNote, Body: "dry-lesson-7",
	}); err != nil {
		t.Fatal(err)
	}
	dr := h.eng.DryRunDefinition(h.eng.defs[name], nil)
	if dr.Memory == nil || !dr.Memory.On || !strings.Contains(dr.Memory.Text, "dry-lesson-7") {
		t.Fatalf("dry run memory: %+v", dr.Memory)
	}
}
