package engine

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/registry"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func fallbackCatalog(ps ...catalog.Provider) *catalog.Catalog {
	c := mockCatalog()
	c.Providers = registry.New[catalog.Provider]("provider",
		append([]catalog.Provider{{Name: "mock", Kind: catalog.KindMock}}, ps...)...)
	return c
}

// runOn runs a one-step workflow on provider and returns the run's status.
func runOn(t *testing.T, provider string, cat *catalog.Catalog, global []string) string {
	t.Helper()
	def := &workflow.Definition{
		Name: "fallback",
		Steps: []workflow.Step{{
			ID: "only", Provider: provider, Prompt: "say something",
			OutputSchema: objSchema([]any{"finding"}, map[string]any{"finding": strProp()}),
		}},
	}
	normalizeForTest(def)
	st, bl, cfg := testDeps(t)
	cfg.WorkDir = t.TempDir()
	cfg.FallbackProviders = global
	eng := New(cfg, st, bl, map[string]*workflow.Definition{def.Name: def}, skills.Load(t.TempDir()), cat)
	h := &harness{eng: eng, store: st, t: t}
	t.Cleanup(func() {
		for _, id := range h.runs {
			eng.Cancel(context.Background(), id)
		}
		time.Sleep(50 * time.Millisecond)
	})
	run := h.run(nil)
	for id, s := range h.steps(run.ID) {
		t.Logf("step %s = %s: %v", id, s.Status, s.Error)
	}
	return run.Status
}

// A provider whose key is not set does not resolve; its own fallback list
// takes the step instead of failing the run.
func TestAProviderThatDoesNotResolveFallsBackToItsOwnList(t *testing.T) {
	cat := fallbackCatalog(catalog.Provider{Name: "broken", Kind: catalog.KindHTTP,
		BaseURL: "http://127.0.0.1:1", Style: "openai", Model: "x",
		APIKeyEnv: "WFX_TEST_KEY_THAT_IS_NEVER_SET", Fallback: []string{"mock"}})
	if got := runOn(t, "broken", cat, nil); got != "done" {
		t.Fatalf("run = %s, want done on the fallback provider", got)
	}
}

// The server-wide list is the one place an operator fixes a broken provider
// for every workflow: no workflow and no provider entry has to change.
func TestTheServerWideFallbackFixesEveryWorkflow(t *testing.T) {
	cat := fallbackCatalog(catalog.Provider{Name: "broken", Kind: catalog.KindHTTP,
		BaseURL: "http://127.0.0.1:1", Style: "openai", Model: "x",
		APIKeyEnv: "WFX_TEST_KEY_THAT_IS_NEVER_SET"})
	if got := runOn(t, "broken", cat, []string{"mock"}); got != "done" {
		t.Fatalf("run = %s, want done on the server-wide fallback", got)
	}
}

// A provider that resolves but fails its first call (nothing listening — the
// port clash, a dead endpoint) never answered, so the step moves on.
func TestAProviderThatFailsBeforeAnsweringFallsBack(t *testing.T) {
	cat := fallbackCatalog(catalog.Provider{Name: "dead", Kind: catalog.KindHTTP,
		BaseURL: "http://127.0.0.1:1", Style: "openai", Model: "x", Fallback: []string{"mock"}})
	if got := runOn(t, "dead", cat, nil); got != "done" {
		t.Fatalf("run = %s, want done on the fallback provider", got)
	}
}

// With no fallback anywhere a broken provider still fails the run, plainly.
func TestNoFallbackStillFails(t *testing.T) {
	cat := fallbackCatalog(catalog.Provider{Name: "broken", Kind: catalog.KindHTTP,
		BaseURL: "http://127.0.0.1:1", Style: "openai", Model: "x",
		APIKeyEnv: "WFX_TEST_KEY_THAT_IS_NEVER_SET"})
	if got := runOn(t, "broken", cat, nil); got == "done" {
		t.Fatal("a broken provider with no fallback must not report done")
	}
}

func TestTheChainIsOrderedDedupedAndSkipsUnknownNames(t *testing.T) {
	cat := fallbackCatalog(
		catalog.Provider{Name: "a", Kind: catalog.KindMock, Fallback: []string{"b", "nope", "a"}},
		catalog.Provider{Name: "b", Kind: catalog.KindMock},
		catalog.Provider{Name: "c", Kind: catalog.KindMock},
	)
	st, bl, cfg := testDeps(t)
	cfg.FallbackProviders = []string{"c", "b"}
	eng := New(cfg, st, bl, nil, skills.Load(t.TempDir()), cat)
	got := eng.providerChain(&workflow.Step{Provider: "a"})
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	// An eval pins one backend: falling back would measure a different one.
	eng.UseProvider("c")
	if got := eng.providerChain(&workflow.Step{Provider: "a"}); !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("an override must pin the chain, got %v", got)
	}
}

// Every ACP agent opens a fresh session per turn unless it opts out: each
// turn's prompt is the complete request, so a kept session pays for every
// earlier turn again.
func TestEveryACPProviderGetsAFreshSessionPerTurnByDefault(t *testing.T) {
	for _, preset := range []string{"devin", "opencode", "claude", "codex"} {
		cfg, err := acpConfig(catalog.Provider{Name: preset, Kind: catalog.KindACP, Preset: preset}, "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.SessionPerTurn {
			t.Errorf("%s: a fresh session per turn must be the default", preset)
		}
	}
	cfg, _ := acpConfig(catalog.Provider{Name: "k", Kind: catalog.KindACP, Preset: "devin", KeepSession: true}, "", "", nil)
	if cfg.SessionPerTurn {
		t.Error("keepSession must keep one session")
	}
}
