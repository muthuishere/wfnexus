package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func probeEngine(t *testing.T, p catalog.Provider) *Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := testEngine(t, nil).cfg
	cfg.WorkDir, cfg.DefaultProvider = dir, p.Name
	return New(cfg, nil, nil, map[string]*workflow.Definition{}, skills.Load(dir), testCatalog(t, p))
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The bug this exists for: a provider on PATH that refuses every call. The
// probe must say FAIL and carry the provider's own words.
func TestProbeDefaultReportsTheProvidersRefusal(t *testing.T) {
	bin := script(t, "cat >/dev/null; echo \"Error: OpenCode's free tier can only be used from within OpenCode\" >&2; exit 1\n")
	e := probeEngine(t, catalog.Provider{Name: "agent", Kind: catalog.KindCLI, Command: []string{bin, "{{prompt}}"}})
	got := e.ProbeDefault(context.Background())
	if got.OK || !strings.Contains(got.Error, "free tier can only be used") {
		t.Fatalf("want FAIL with the provider's error, got %+v", got)
	}
}

func TestProbeDefaultPassesOnAnAnswer(t *testing.T) {
	bin := script(t, "echo PONG\n")
	e := probeEngine(t, catalog.Provider{Name: "agent", Kind: catalog.KindCLI, Command: []string{bin, "{{prompt}}"}})
	got := e.ProbeDefault(context.Background())
	if !got.OK || got.Reply != "PONG" || got.Provider != "agent" {
		t.Fatalf("want OK PONG, got %+v", got)
	}
}

func TestProbeDefaultMissingKey(t *testing.T) {
	t.Setenv("WFX_PROBE_TEST_KEY", "")
	e := probeEngine(t, catalog.Provider{Name: "h", Kind: catalog.KindHTTP, BaseURL: "http://127.0.0.1:1", Style: "openai", Model: "m", APIKeyEnv: "WFX_PROBE_TEST_KEY"})
	if got := e.ProbeDefault(context.Background()); got.OK || !strings.Contains(got.Error, "WFX_PROBE_TEST_KEY") {
		t.Fatalf("want a missing-key failure, got %+v", got)
	}
}

// Live: the shipped default, for real. WFX_LIVE=1 go test -run LiveProbe -v ./internal/engine/
func TestLiveProbeOpenCodeDefault(t *testing.T) {
	if os.Getenv("WFX_LIVE") != "1" {
		t.Skip("set WFX_LIVE=1 to call the real opencode CLI")
	}
	for _, m := range []string{"opencode/ling-3.1-flash-free", "opencode/nope-free"} {
		e := probeEngine(t, catalog.Provider{Name: "opencode", Kind: catalog.KindCLI, Preset: "opencode", Model: m})
		got := e.ProbeDefault(context.Background())
		t.Logf("%s: ok=%v %dms reply=%q error=%q", m, got.OK, got.Ms, got.Reply, got.Error)
		if got.OK != (m != "opencode/nope-free") {
			t.Errorf("%s: unexpected ok=%v", m, got.OK)
		}
	}
}

func TestFailureLineIsAnErrorWithRunAndReason(t *testing.T) {
	id := uuid.New()
	line := failureLine(id, "triage", "opencode: exit status 1")
	if !strings.HasPrefix(line, "ERROR ") || !strings.Contains(line, id.String()) ||
		!strings.Contains(line, "triage") || !strings.Contains(line, "exit status 1") {
		t.Fatalf("bad failure line: %q", line)
	}
}
