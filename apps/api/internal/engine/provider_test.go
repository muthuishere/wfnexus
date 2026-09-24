package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func testCatalog(t *testing.T, ps ...catalog.Provider) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		c.Providers.Add(p, "test")
	}
	return c
}

func testEngine(t *testing.T, c *catalog.Catalog) *Engine {
	t.Helper()
	return &Engine{
		cfg: config.Config{
			LLMBaseURL: "https://default.invalid/v1", LLMStyle: "openai",
			Model: "default-model", LLMAPIKeyEnv: "WFX_TEST_DEFAULT_KEY",
		},
		catalog: c,
	}
}

// A step that names no provider keeps the pre-provider behaviour exactly.
func TestNoProviderUsesTheProcessDefault(t *testing.T) {
	e := testEngine(t, testCatalog(t))
	got, err := e.resolveLLM(&workflow.Step{ID: "s"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.BaseURL != "https://default.invalid/v1" || got.LLM.Model != "default-model" || got.Label != "default-model" {
		t.Fatalf("got %+v label=%q", got.LLM, got.Label)
	}

	// `model:` alone still overrides the model id against the default endpoint.
	got, err = e.resolveLLM(&workflow.Step{ID: "s", Model: "other"}, "")
	if err != nil || got.LLM.Model != "other" || got.LLM.BaseURL != "https://default.invalid/v1" {
		t.Fatalf("got %+v err=%v", got.LLM, err)
	}
}

// The regression this file exists for: a named provider must SELECT the
// endpoint. It used to be validated at load time and then ignored, so a step
// naming a provider ran on the process default and said nothing.
func TestANamedHTTPProviderSelectsItsEndpoint(t *testing.T) {
	t.Setenv("WFX_TEST_ALT_KEY", "not-a-real-key")
	e := testEngine(t, testCatalog(t, catalog.Provider{
		Name: "alt", Kind: catalog.KindHTTP, BaseURL: "https://alt.invalid/v1",
		Style: "anthropic", Model: "alt-model", APIKeyEnv: "WFX_TEST_ALT_KEY",
	}))

	got, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: "alt"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.BaseURL != "https://alt.invalid/v1" || got.LLM.Model != "alt-model" || string(got.LLM.Style) != "anthropic" {
		t.Fatalf("provider was named and not used: %+v", got.LLM)
	}
	if got.Label != "alt/alt-model" {
		t.Fatalf("label = %q — it must say which provider actually ran", got.Label)
	}

	// `model:` beside a provider picks a model WITHIN it, never past it.
	got, err = e.resolveLLM(&workflow.Step{ID: "s", Provider: "alt", Model: "alt-mini"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.Model != "alt-mini" || got.LLM.BaseURL != "https://alt.invalid/v1" {
		t.Fatalf("model override escaped the provider: %+v", got.LLM)
	}
}

// Named but not held: said at resolve time, not discovered as a 401 twenty
// turns in. The message may name the VARIABLE and never its value.
func TestAMissingKeyIsRefusedByName(t *testing.T) {
	e := testEngine(t, testCatalog(t, catalog.Provider{
		Name: "alt", Kind: catalog.KindHTTP, BaseURL: "https://alt.invalid/v1",
		Style: "openai", Model: "alt-model", APIKeyEnv: "WFX_TEST_ABSENT_KEY",
	}))
	_, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: "alt"}, "")
	if err == nil || !strings.Contains(err.Error(), "WFX_TEST_ABSENT_KEY") {
		t.Fatalf("err = %v, want it to name the variable", err)
	}
}

// A local-process provider resolves to a TRANSPORT rather than an endpoint:
// the model is a program on this machine, so there is no URL to dial and no key
// to hold. This is what `cli` and `acp` providers could not do before v0.19.0
// exported toolnexus's in-process round tripper — they were validated, then
// refused.
func TestALocalProviderResolvesToATransport(t *testing.T) {
	e := testEngine(t, testCatalog(t,
		catalog.Provider{Name: "devin", Kind: catalog.KindACP, Preset: "devin", TimeoutSec: 900},
		catalog.Provider{Name: "claude-cli", Kind: catalog.KindCLI, Preset: "claude", Repairs: 2},
		catalog.Provider{Name: "copilot-cli", Kind: catalog.KindCLI, Preset: "copilot"},
		catalog.Provider{Name: "any-cli", Kind: catalog.KindCLI, Command: []string{"mycli", "-p"}},
	))
	for _, name := range []string{"devin", "claude-cli", "copilot-cli", "any-cli"} {
		got, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: name}, t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Cleanup(got.Close)
		if got.Transport == nil {
			t.Errorf("%s: no transport — the step would go to the network instead of the process", name)
		}
		if got.LLM.APIKey == "" {
			t.Errorf("%s: empty key; the client resolves one from the environment and fails when it finds none", name)
		}
		if !strings.HasPrefix(got.Label, name+"/") {
			t.Errorf("%s: label %q does not say which provider ran", name, got.Label)
		}
	}

	// An http provider must NOT get a transport: it has a real endpoint.
	t.Setenv("WFX_TEST_HTTP_KEY", "not-a-real-key")
	e = testEngine(t, testCatalog(t, catalog.Provider{
		Name: "h", Kind: catalog.KindHTTP, BaseURL: "https://h.invalid/v1",
		Style: "openai", Model: "m", APIKeyEnv: "WFX_TEST_HTTP_KEY",
	}))
	got, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: "h"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Transport != nil {
		t.Fatal("an http provider was given an in-process transport")
	}
}

// An unknown preset is refused at resolve time. A guessed argv template fails
// as a parse error twenty turns in, which is the worst place to learn the name
// was never recognised.
func TestAnUnknownPresetIsRefusedNotGuessed(t *testing.T) {
	e := testEngine(t, testCatalog(t,
		catalog.Provider{Name: "opencode", Kind: catalog.KindCLI, Preset: "opencode"},
	))
	_, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: "opencode"}, "")
	if err == nil {
		t.Fatal("an unknown preset resolved")
	}
	if !strings.Contains(err.Error(), "opencode") || !strings.Contains(err.Error(), "command") {
		t.Fatalf("error should name the preset and the way out: %v", err)
	}
}

func TestAnUnknownProviderIsRefused(t *testing.T) {
	e := testEngine(t, testCatalog(t))
	if _, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: "nope"}, ""); err == nil {
		t.Fatal("unknown provider resolved")
	}
}

// A declared `default:` in an input_schema used to be decoration: the input was
// stored exactly as posted, so a live run rendered the literal "<no value>"
// into its prompt and the agent said so in its own output. Defaults are filled
// BEFORE validation, so a default can satisfy `required`.
func TestInputDefaultsAreAppliedThenValidated(t *testing.T) {
	def := &workflow.Definition{
		Name: "withdefaults",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"question", "repo"},
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "default": "what does this do?"},
				"repo":     map[string]any{"type": "string"},
				"depth":    map[string]any{"type": "integer", "default": float64(3)},
			},
		},
	}
	e := &Engine{defs: map[string]*workflow.Definition{"withdefaults": def}}

	raw, err := e.PrepareInput("withdefaults", []byte(`{"repo":"/tmp/x"}`))
	if err != nil {
		t.Fatalf("the default should have satisfied `required`: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["question"] != "what does this do?" {
		t.Fatalf("default not applied: %v", got)
	}
	if got["depth"] != float64(3) {
		t.Fatalf("non-string default not applied: %v", got)
	}

	// A supplied value always wins over the default.
	raw, _ = e.PrepareInput("withdefaults", []byte(`{"repo":"/tmp/x","question":"mine"}`))
	_ = json.Unmarshal(raw, &got)
	if got["question"] != "mine" {
		t.Fatalf("default overwrote a supplied value: %v", got)
	}

	// A genuinely missing required field is refused BEFORE a run exists, and the
	// message names the workflow rather than talking about "output".
	if _, err := e.PrepareInput("withdefaults", []byte(`{}`)); err == nil {
		t.Fatal("missing required input accepted")
	} else if !strings.Contains(err.Error(), "withdefaults input") || !strings.Contains(err.Error(), "repo") {
		t.Fatalf("unhelpful message: %v", err)
	}
}

// `kind` decides the backend, not `command`. An acp entry that names its
// binary used to be run as a one-shot CommandAgent — the command test came
// first — so the process never spoke ACP and the entry's own kind was ignored.
// The two backends are distinguishable without starting a process: only ACP
// returns a Close that does real work, and only ACP is a *ACPAgent.
func TestAnACPEntryWithACommandStillSpeaksACP(t *testing.T) {
	for _, p := range []catalog.Provider{
		{Name: "acp-cmd", Kind: catalog.KindACP, Command: []string{"myacp", "acp"}, TimeoutSec: 900},
		{Name: "acp-preset", Kind: catalog.KindACP, Preset: "devin", TimeoutSec: 900},
	} {
		agent, closeFn, err := localAgent(p, "m", t.TempDir(), nil)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		t.Cleanup(closeFn)
		if _, ok := agent.(*devinadapter.ACPAgent); !ok {
			t.Fatalf("%s: kind=acp resolved to %T — the command was run as a one-shot CLI and ACP was never spoken", p.Name, agent)
		}
	}

	// A cli entry with a command is still the generic CommandAgent: the fix
	// must not steal the path ADR 0016 calls the real mechanism.
	agent, _, err := localAgent(
		catalog.Provider{Name: "any-cli", Kind: catalog.KindCLI, Command: []string{"mycli", "-p", "{{prompt}}"}},
		"m", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agent.(*devinadapter.CommandAgent); !ok {
		t.Fatalf("a cli command resolved to %T", agent)
	}
}
