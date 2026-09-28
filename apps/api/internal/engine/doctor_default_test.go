package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// fakeBin puts an executable called name on a fresh PATH.
func fakeBin(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// Found rehearsing the compose stack: the default provider opencode-acp was
// reported "not ready ()", an empty reason, for the PRESENT state that the
// provider list beside it calls a note. The default gets the same rule.
func TestADefaultProviderThatIsPresentIsNotAProblem(t *testing.T) {
	dir := t.TempDir()
	cfg := testEngine(t, nil).cfg
	cfg.WorkDir, cfg.DefaultProvider = dir, "agent"
	e := New(cfg, nil, nil, map[string]*workflow.Definition{}, skills.Load(dir),
		testCatalog(t, catalog.Provider{Name: "agent", Kind: catalog.KindCLI, Command: []string{"sh"}}))
	for _, p := range e.Doctor().Problems {
		if strings.Contains(p, "default provider") {
			t.Fatalf("a present default provider was reported as a problem: %s", p)
		}
	}
}

// opencode on a Zen "-free" model needs no account, so there is no login to be
// missing: it is ready. On any other model it keeps the unchecked caveat.
func TestOpencodeOnAFreeModelIsReady(t *testing.T) {
	fakeBin(t, "opencode")
	free := catalog.Provider{Name: "opencode-acp", Kind: catalog.KindACP, Preset: "opencode", Model: "opencode/space-bunny-free"}
	if got := checkProvider(free); !got.Ready || got.State != "ready" {
		t.Fatalf("opencode on a free model: ready=%v state=%q", got.Ready, got.State)
	}
	paid := free
	paid.Model = "anthropic/claude-sonnet-4.5"
	if got := checkProvider(paid); got.Ready {
		t.Fatal("opencode on a paid model was reported ready without a login check")
	}
}

// The store-aware check must still refuse a key that is nowhere: the judge
// reads its key by name at run time, and an empty name is a 401 mid-run.
func TestAClassifierWithNoKeyAnywhereIsNotReady(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dir := t.TempDir()
	cfg := testEngine(t, nil).cfg
	cfg.WorkDir = dir
	cat := testCatalog(t)
	cat.Classifiers.Add(catalog.Classifier{Name: "jev-direct", Backend: "typesafe"}, "test")
	e := New(cfg, nil, nil, map[string]*workflow.Definition{}, skills.Load(dir), cat)
	for _, c := range e.Doctor().Classifiers {
		if c.Name == "jev-direct" && c.Ready {
			t.Fatal("no key anywhere, yet ready")
		}
	}
}
