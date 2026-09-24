package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// compaction is OFF unless the step's budget asks for it, so every workflow
// shipped before this change is byte-identical to before.
func TestCompactAtTokensDefaultsOff(t *testing.T) {
	if got := compactAtTokens(&workflow.Step{}); got != 0 {
		t.Fatalf("no budget: want 0, got %d", got)
	}
	if got := compactAtTokens(&workflow.Step{Budget: &workflow.Budget{MaxTurns: 40}}); got != 0 {
		t.Fatalf("budget without compact_at_tokens: want 0, got %d", got)
	}
	if got := compactAtTokens(&workflow.Step{Budget: &workflow.Budget{CompactAtTokens: 120000}}); got != 120000 {
		t.Fatalf("want 120000, got %d", got)
	}
}

// The knob rides the EXISTING budget surface, so it parses in both dialects the
// platform already accepts (decode.go): the file's `compact_at_tokens` and the
// API's `compactAtTokens`. That is what lets a small org set it in a checked-in
// YAML and an enterprise set it through the API without a second config shape.
func TestCompactAtTokensParsedInBothDialects(t *testing.T) {
	for _, body := range []string{
		`{"name":"c","steps":[{"id":"s","prompt":"hi","budget":{"max_turns":5,"compact_at_tokens":90000}}]}`,
		`{"name":"c","steps":[{"id":"s","prompt":"hi","budget":{"maxTurns":5,"compactAtTokens":90000}}]}`,
	} {
		def, err := workflow.DecodeDefinition([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if got := compactAtTokens(&def.Steps[0]); got != 90000 {
			t.Fatalf("%s: want 90000, got %d", body, got)
		}
		if def.Steps[0].Budget.MaxTurns != 5 {
			t.Fatalf("%s: the rest of the budget must survive", body)
		}
	}
}

func TestCompactAtTokensParsedFromYAML(t *testing.T) {
	var def workflow.Definition
	if err := yaml.Unmarshal([]byte("name: c\nsteps:\n  - id: s\n    prompt: hi\n    budget: { max_turns: 5, compact_at_tokens: 90000 }\n"), &def); err != nil {
		t.Fatal(err)
	}
	if got := compactAtTokens(&def.Steps[0]); got != 90000 {
		t.Fatalf("want 90000, got %d", got)
	}
}

// chainBeforeLLM threads each override's messages into the next hook, so the
// LAST hook sees what the model will actually receive. That is what keeps the
// turn-budget warning from being summarized away the instant it is added.
func TestChainBeforeLLMThreadsMessages(t *testing.T) {
	first := func(_ context.Context, ev tn.BeforeLLMEvent) (*tn.LLMOverride, error) {
		return &tn.LLMOverride{Messages: []any{map[string]any{"role": "system", "content": "compacted"}}}, nil
	}
	var seen []any
	second := func(_ context.Context, ev tn.BeforeLLMEvent) (*tn.LLMOverride, error) {
		seen = ev.Messages
		return &tn.LLMOverride{Messages: append(append([]any{}, ev.Messages...),
			map[string]any{"role": "user", "content": "BUDGET"})}, nil
	}
	ov, err := chainBeforeLLM(first, second)(context.Background(), tn.BeforeLLMEvent{
		Messages: []any{map[string]any{"role": "user", "content": "original"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].(map[string]any)["content"] != "compacted" {
		t.Fatalf("second hook did not see the first hook's override: %v", seen)
	}
	if len(ov.Messages) != 2 || ov.Messages[1].(map[string]any)["content"] != "BUDGET" {
		t.Fatalf("final override lost the warning: %v", ov.Messages)
	}
}

func TestChainBeforeLLMSkipsNilAndPassesThrough(t *testing.T) {
	if chainBeforeLLM(nil, nil) != nil {
		t.Fatal("all-nil chain should be nil so the agent keeps no hook at all")
	}
	only := func(_ context.Context, _ tn.BeforeLLMEvent) (*tn.LLMOverride, error) { return nil, nil }
	if chainBeforeLLM(nil, only) == nil {
		t.Fatal("a single live hook should survive the chain")
	}
	ov, err := chainBeforeLLM(only, only)(context.Background(), tn.BeforeLLMEvent{})
	if err != nil || ov != nil {
		t.Fatalf("hooks that decline should yield a nil override: %v %v", ov, err)
	}
}

func TestChainBeforeLLMPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	fail := func(_ context.Context, _ tn.BeforeLLMEvent) (*tn.LLMOverride, error) { return nil, boom }
	ran := false
	after := func(_ context.Context, _ tn.BeforeLLMEvent) (*tn.LLMOverride, error) { ran = true; return nil, nil }
	if _, err := chainBeforeLLM(fail, after)(context.Background(), tn.BeforeLLMEvent{}); !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if ran {
		t.Fatal("a failed hook must short-circuit the chain")
	}
}

// A step that does not ask for compaction gets no hook, which is how the
// one-person default (a single binary, no tuning) stays exactly as it was.
func TestCompactorHookNilWhenDisabled(t *testing.T) {
	var e Engine
	if h := e.compactorHook(context.Background(), uuid.New(), "s", &workflow.Step{}, resolved{}, nil, nil); h != nil {
		t.Fatal("want nil hook when compaction is off")
	}
}
