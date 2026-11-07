package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func autoCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	p := filepath.Join(t.TempDir(), "registries.json")
	os.WriteFile(p, []byte(`{"providers":{
	  "claude-cli":{"kind":"cli","preset":"claude","model":"sonnet"},
	  "codex-cli":{"kind":"cli","command":["codex","exec","{{prompt}}"]},
	  "openrouter":{"kind":"http","baseUrl":"https://openrouter.ai/api/v1","style":"openai","model":"x","apiKeyEnv":"OPENROUTER_API_KEY"}}}`), 0o644)
	cat, err := catalog.Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func fakeBins(t *testing.T, names ...string) {
	dir := t.TempDir()
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755)
	}
	t.Setenv("PATH", dir)
}

func TestAutoDefaultPrefersClaudeThenCodex(t *testing.T) {
	fakeBins(t, "claude", "codex")
	if e := New(config.Config{AutoProvider: true}, nil, nil, nil, skills.Load(), autoCatalog(t)); e.AutoProvider() != "claude-cli" {
		t.Fatalf("picked %q, want claude-cli", e.AutoProvider())
	}
	fakeBins(t, "codex")
	if e := New(config.Config{AutoProvider: true}, nil, nil, nil, skills.Load(), autoCatalog(t)); e.AutoProvider() != "codex-cli" {
		t.Fatalf("picked %q, want codex-cli", e.AutoProvider())
	}
}

func TestAutoDefaultNeverFallsBackToAnAPI(t *testing.T) {
	fakeBins(t)
	t.Setenv("OPENROUTER_API_KEY", "fake")
	e := New(config.Config{AutoProvider: true}, nil, nil, nil, skills.Load(), autoCatalog(t))
	if e.AutoProvider() != "" {
		t.Fatalf("picked %q with no agent CLI on PATH", e.AutoProvider())
	}
	if _, err := e.resolveLLM(&workflow.Step{ID: "s"}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no agent CLI") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if d := e.Doctor(); len(d.Problems) == 0 || !strings.Contains(strings.Join(d.Problems, "\n"), "no agent CLI") {
		t.Fatalf("doctor should say NOT READY: %v", d.Problems)
	}
}
