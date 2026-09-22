package engine

import (
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func dryEngine(t *testing.T) *Engine {
	t.Helper()
	t.Setenv("WFX_TEST_DRY_KEY", "not-a-real-key")
	c, err := catalog.Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	c.Providers.Add(catalog.Provider{
		Name: "ok-provider", Kind: catalog.KindHTTP, BaseURL: "https://x/v1",
		Style: "openai", Model: "m", APIKeyEnv: "WFX_TEST_DRY_KEY",
	}, "test")
	c.Providers.Add(catalog.Provider{
		Name: "no-key", Kind: catalog.KindHTTP, BaseURL: "https://x/v1",
		Style: "openai", Model: "m", APIKeyEnv: "WFX_TEST_ABSENT_DRY_KEY",
	}, "test")
	return &Engine{
		cfg: config.Config{
			LLMBaseURL: "https://default/v1", LLMStyle: "openai",
			Model: "default-model", LLMAPIKeyEnv: "WFX_TEST_DRY_KEY",
		},
		catalog: c,
	}
}

func agentStep(id, prompt string, props map[string]any) workflow.Step {
	return workflow.Step{
		ID: id, Name: id, Prompt: prompt, MaxTurns: 5,
		OutputSchema: map[string]any{"type": "object", "properties": props},
	}
}

// The whole point: a dry run answers "would this work HERE", which validation
// does not. A provider whose key variable is unset is a perfectly valid file
// and a certain failure.
func TestDryRunCatchesAProviderWithNoKey(t *testing.T) {
	e := dryEngine(t)
	def := &workflow.Definition{
		Name: "wf", InputSchema: map[string]any{"type": "object"},
		Steps: []workflow.Step{agentStep("a", "go", map[string]any{"ok": map[string]any{"type": "boolean"}})},
	}
	def.Steps[0].Provider = "no-key"

	d := e.DryRunDefinition(def, nil)
	if d.OK {
		t.Fatal("a provider with no key passed a dry run")
	}
	if !strings.Contains(d.Summary(), "WFX_TEST_ABSENT_DRY_KEY") {
		t.Fatalf("the summary should name the variable: %s", d.Summary())
	}
}

// Both template failures are SILENT at run time — missingkey=zero renders
// nothing, and stepval returns "" — so a prompt with a typo just loses a
// sentence and nobody finds out. These are the checks that earn the feature.
func TestDryRunCatchesReferencesThatWouldRenderEmpty(t *testing.T) {
	e := dryEngine(t)
	def := &workflow.Definition{
		Name: "wf",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"topic": map[string]any{"type": "string"},
		}},
		Steps: []workflow.Step{
			agentStep("first", "about {{ .Input.topic }}", map[string]any{
				"summary": map[string]any{"type": "string"},
			}),
			agentStep("second", "read {{ .Steps.first.summary }} and {{ .Steps.first.nope }} for {{ .Input.absent }}", nil),
		},
	}
	d := e.DryRunDefinition(def, nil)

	var msgs []string
	for _, p := range d.Problems {
		msgs = append(msgs, p.Message)
	}
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "first.nope") {
		t.Errorf("a field absent from the output schema was not reported:\n%s", joined)
	}
	if !strings.Contains(joined, "absent") {
		t.Errorf("an input the schema does not declare was not reported:\n%s", joined)
	}
	// The one that DOES exist must not be reported, or the check is noise.
	if strings.Contains(joined, "first.summary") {
		t.Errorf("a valid reference was reported:\n%s", joined)
	}
}

// Reading a step that has not run yet is never intentional and always renders
// empty, so it is fatal rather than a warning.
func TestDryRunCatchesReadingALaterStep(t *testing.T) {
	e := dryEngine(t)
	def := &workflow.Definition{
		Name: "wf", InputSchema: map[string]any{"type": "object"},
		Steps: []workflow.Step{
			agentStep("first", "reads {{ .Steps.second.answer }}", nil),
			agentStep("second", "go", map[string]any{"answer": map[string]any{"type": "string"}}),
		},
	}
	d := e.DryRunDefinition(def, nil)
	if d.OK {
		t.Fatal("reading a later step passed")
	}
	if !strings.Contains(d.Summary(), "runs AFTER") {
		t.Fatalf("the message should say why: %s", d.Summary())
	}
}

// A job's steps are flattened to `job.step`, so an id contains dots. A checker
// that split the reference at the first dot reported every one of them as an
// undefined step — a false positive on real workflows, which is worse than no
// check because it teaches people to ignore the output.
func TestDryRunUnderstandsDottedJobStepIds(t *testing.T) {
	e := dryEngine(t)
	def := &workflow.Definition{
		Name: "wf", InputSchema: map[string]any{"type": "object"},
		Steps: []workflow.Step{
			agentStep("suite.test", "go", map[string]any{"exitCode": map[string]any{"type": "integer"}}),
			agentStep("later.look", "exit was {{ .Steps.suite.test.exitCode }}", nil),
		},
	}
	def.Steps[1].Needs = []string{"suite.test"}

	d := e.DryRunDefinition(def, nil)
	for _, p := range d.Problems {
		if strings.Contains(p.Message, "does not define") {
			t.Fatalf("a flattened job step read as undefined: %s", p.Message)
		}
	}
	if !d.OK {
		t.Fatalf("a valid workflow failed: %s", d.Summary())
	}
}

// A reference guarded by `if` is an explicit statement that it may be absent.
// Warning about it is noise, and a noisy checker gets ignored.
func TestDryRunDoesNotWarnAboutGuardedOptionals(t *testing.T) {
	e := dryEngine(t)
	def := &workflow.Definition{
		Name: "wf", InputSchema: map[string]any{"type": "object"},
		Steps: []workflow.Step{
			agentStep("a", "{{ if .Input.answers }}given: {{ .Input.answers }}{{ end }}", nil),
		},
	}
	d := e.DryRunDefinition(def, nil)
	if len(d.Problems) != 0 {
		t.Fatalf("a guarded optional was reported: %+v", d.Problems)
	}
}

// The waves are the ENGINE's order, and the cost is a ceiling rather than a
// guess, because every budget is declared.
func TestDryRunReportsOrderAndCeiling(t *testing.T) {
	e := dryEngine(t)
	def := &workflow.Definition{
		Name: "wf", InputSchema: map[string]any{"type": "object"},
		Steps: []workflow.Step{
			agentStep("a", "go", nil),
			agentStep("b", "go", nil),
			agentStep("c", "go", nil),
		},
	}
	def.Steps[1].Needs = []string{"a"}
	def.Steps[2].Needs = []string{"a"}

	d := e.DryRunDefinition(def, nil)
	if d.Shape != "parallel" {
		t.Fatalf("shape = %q", d.Shape)
	}
	if len(d.Waves) != 2 || len(d.Waves[0]) != 1 || len(d.Waves[1]) != 2 {
		t.Fatalf("waves = %v, want [[a] [b c]]", d.Waves)
	}
	if d.Cost.MaxTurns != 15 || d.Cost.AgentSteps != 3 {
		t.Fatalf("cost = %+v, want 15 turns over 3 agent steps", d.Cost)
	}
}

// A `run` step calls no model, and saying so is the point: it is the part of a
// workflow that costs nothing.
func TestDryRunSeparatesFreeSteps(t *testing.T) {
	e := dryEngine(t)
	def := &workflow.Definition{
		Name: "wf", InputSchema: map[string]any{"type": "object"},
		Steps: []workflow.Step{
			{ID: "check", Name: "check", Run: "echo hi",
				OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}}},
		},
	}
	d := e.DryRunDefinition(def, nil)
	if d.Cost.AgentSteps != 0 || d.Cost.FreeSteps != 1 {
		t.Fatalf("cost = %+v, want 0 agent steps and 1 free", d.Cost)
	}
	if !strings.Contains(d.Summary(), "call no model") {
		t.Fatalf("summary should say it is free: %s", d.Summary())
	}
}

// A workflow FILE says `output_schema`; the JSON API says `outputSchema`. The
// struct carries both tags, so the decoder decides which applies — and the
// authoring tools must read the file's dialect, because every example an author
// has seen is a file and `wf_catalog kind=shape` describes a file.
//
// Getting this wrong was silent and expensive: a correct step was reported as
// missing the very field it had just declared, four times in a row.
func TestAuthoringToolsReadTheFileDialect(t *testing.T) {
	def, err := decodeDefinition(map[string]any{
		"name": "wf",
		"steps": []any{map[string]any{
			"id": "a", "prompt": "go",
			"max_turns":     float64(7),
			"output_schema": map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Steps) != 1 {
		t.Fatalf("steps = %d", len(def.Steps))
	}
	if def.Steps[0].OutputSchema == nil {
		t.Fatal("output_schema was dropped — the step would be reported as having none")
	}
	if def.Steps[0].MaxTurns != 7 {
		t.Fatalf("max_turns = %d, want 7", def.Steps[0].MaxTurns)
	}
}

// A guessed field name must be reported where it was written. yaml and json
// both drop an unknown key silently, so without this the failure surfaces later
// as something unrelated.
func TestAuthoringToolsRejectGuessedFieldNames(t *testing.T) {
	msg := unknownFieldsIn(map[string]any{
		"name": "wf",
		"steps": []any{map[string]any{
			"id": "a", "title": "A", "type": "agent", "prompt": "go",
		}},
	})
	for _, want := range []string{"title", "type", "shape"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q: %s", want, msg)
		}
	}
	if got := unknownFieldsIn(map[string]any{
		"steps": []any{map[string]any{"id": "a", "prompt": "go", "output_schema": map[string]any{}}},
	}); got != "" {
		t.Fatalf("real fields reported as unknown: %s", got)
	}
}
