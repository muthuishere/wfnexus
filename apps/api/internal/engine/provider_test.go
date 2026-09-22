package engine

import (
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
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
	llm, label, err := e.resolveLLM(&workflow.Step{ID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if llm.BaseURL != "https://default.invalid/v1" || llm.Model != "default-model" || label != "default-model" {
		t.Fatalf("got %+v label=%q", llm, label)
	}

	// `model:` alone still overrides the model id against the default endpoint.
	llm, _, err = e.resolveLLM(&workflow.Step{ID: "s", Model: "other"})
	if err != nil || llm.Model != "other" || llm.BaseURL != "https://default.invalid/v1" {
		t.Fatalf("got %+v err=%v", llm, err)
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

	llm, label, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: "alt"})
	if err != nil {
		t.Fatal(err)
	}
	if llm.BaseURL != "https://alt.invalid/v1" || llm.Model != "alt-model" || string(llm.Style) != "anthropic" {
		t.Fatalf("provider was named and not used: %+v", llm)
	}
	if label != "alt/alt-model" {
		t.Fatalf("label = %q — it must say which provider actually ran", label)
	}

	// `model:` beside a provider picks a model WITHIN it, never past it.
	llm, _, err = e.resolveLLM(&workflow.Step{ID: "s", Provider: "alt", Model: "alt-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if llm.Model != "alt-mini" || llm.BaseURL != "https://alt.invalid/v1" {
		t.Fatalf("model override escaped the provider: %+v", llm)
	}
}

// Named but not held: said at resolve time, not discovered as a 401 twenty
// turns in. The message may name the VARIABLE and never its value.
func TestAMissingKeyIsRefusedByName(t *testing.T) {
	e := testEngine(t, testCatalog(t, catalog.Provider{
		Name: "alt", Kind: catalog.KindHTTP, BaseURL: "https://alt.invalid/v1",
		Style: "openai", Model: "alt-model", APIKeyEnv: "WFX_TEST_ABSENT_KEY",
	}))
	_, _, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: "alt"})
	if err == nil || !strings.Contains(err.Error(), "WFX_TEST_ABSENT_KEY") {
		t.Fatalf("err = %v, want it to name the variable", err)
	}
}

// A provider we cannot run yet must REFUSE. Falling back to the default model
// is the exact bug this resolution was written to remove: the run would look
// successful and the named provider would never have been consulted.
func TestAnUnrunnableProviderRefusesInsteadOfFallingBack(t *testing.T) {
	e := testEngine(t, testCatalog(t,
		catalog.Provider{Name: "devin", Kind: catalog.KindACP, Preset: "devin"},
		catalog.Provider{Name: "claude-cli", Kind: catalog.KindCLI, Preset: "claude"},
	))
	for _, name := range []string{"devin", "claude-cli"} {
		llm, _, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: name})
		if err == nil {
			t.Fatalf("%s: resolved to %+v, want a refusal", name, llm)
		}
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: error does not name the provider: %v", name, err)
		}
	}
}

func TestAnUnknownProviderIsRefused(t *testing.T) {
	e := testEngine(t, testCatalog(t))
	if _, _, err := e.resolveLLM(&workflow.Step{ID: "s", Provider: "nope"}); err == nil {
		t.Fatal("unknown provider resolved")
	}
}
