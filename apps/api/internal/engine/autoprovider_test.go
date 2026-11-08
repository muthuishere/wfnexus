package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/agentadapter"

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

func TestAutoDefaultSkipsALoggedOutClaude(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n[ \"$1\" = auth ] && echo '{\"loggedIn\": false}'\nexit 0\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	t.Setenv("PATH", dir)
	e := New(config.Config{AutoProvider: true}, nil, nil, nil, skills.Load(), autoCatalog(t))
	if e.AutoProvider() != "codex-cli" {
		t.Fatalf("picked %q, want codex-cli past a logged-out claude", e.AutoProvider())
	}
	if len(e.autoSkipped) != 1 || !strings.Contains(e.autoSkipped[0], "not logged in") {
		t.Fatalf("skipped = %v", e.autoSkipped)
	}
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

func TestCLIReportedUsageReplacesTheEstimate(t *testing.T) {
	u := newUsageAccum(pricing{Known: true, Source: "cli"}, func(map[string]any, int) {})
	u.reported = &cliUsage{source: "claude-cli"}
	u.reported.add(agentadapter.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 7, CacheWriteTokens: 3, CostUSD: 0.0125})
	u.reported.add(agentadapter.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.001})
	snap, _ := u.final()
	if snap["promptTokens"] != 120 || snap["completionTokens"] != 25 || snap["totalTokens"] != 145 || snap["costUsd"] != 0.0135 {
		t.Fatalf("snapshot = %v", snap)
	}
}
