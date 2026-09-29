package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

// The workflow names `real` — an HTTP provider whose key is deliberately
// unset, so it could never run here. Every cell passing therefore proves the
// override, not the YAML, chose the backend.
const evalWorkflow = `name: evaldemo
steps:
  - id: survey
    prompt: report one finding
    provider: real
    model: some-real-model
    output_schema:
      type: object
      required: [finding, severity]
      properties:
        finding: {type: string}
        severity: {type: string, enum: [low, medium, high]}
  - id: act
    prompt: act on {{.Steps.survey.finding}}
    provider: real
    needs: [survey]
    requires_approval: true
    output_schema:
      type: object
      required: [done]
      properties:
        done: {type: boolean}
`

const evalRegistries = `{
  "providers": {
    "real":  {"kind": "http", "baseUrl": "https://real.invalid/v1", "style": "openai", "model": "m", "apiKeyEnv": "WFX_EVAL_TEST_NEVER_SET"},
    "mock":  {"kind": "mock"},
    "mock2": {"kind": "mock"}
  }
}`

func localConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	wf := filepath.Join(root, "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "evaldemo.yaml"), []byte(evalWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(root, "registries.json")
	if err := os.WriteFile(reg, []byte(evalRegistries), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Load()
	cfg.WorkflowsDir, cfg.RegistriesPath, cfg.McpConfig = wf, reg, ""
	cfg.TemplatesDir, cfg.SkillsDir, cfg.DefaultProvider = "", filepath.Join(root, "skills"), ""
	cfg.WorkDir = filepath.Join(root, "work")
	return cfg
}

func TestTheMatrixRunsEndToEndOnMockProviders(t *testing.T) {
	t.Setenv("WFX_EVAL_TEST_NEVER_SET", "")
	loc, err := NewLocal(localConfig(t), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loc.Close)

	c, err := ParseCorpus([]byte(`
workflow: evaldemo
cases:
  - name: parks-at-the-gate
    status: awaiting_approval
    assert:
      - {step: survey, path: severity, op: in, value: [low, medium, high]}
      - {step: survey, path: finding, op: contains, value: mock}
      - {step: act, op: exists, value: false}
  - name: expects-done
    assert:
      - {step: survey, path: finding, op: exists}
`))
	if err != nil {
		t.Fatal(err)
	}
	m := Run(context.Background(), loc, c, []string{"mock", "mock2", "missing"})

	if len(m.Rows) != 2 || len(m.Totals) != 3 {
		t.Fatalf("shape: %+v", m)
	}
	for _, cell := range m.Rows[0].Cells[:2] {
		if !cell.Pass {
			t.Errorf("%s: %s %s %+v", cell.Provider, cell.Status, cell.Error, cell.Results)
		}
		if cell.Turns == 0 || cell.PromptTokens < 0 {
			t.Errorf("%s: no usage came back: %+v", cell.Provider, cell)
		}
		if !cell.CostKnown || cell.CostUsd != 0 {
			t.Errorf("%s: mock cost must be a known $0, got %v known=%v", cell.Provider, cell.CostUsd, cell.CostKnown)
		}
	}
	// A gated workflow never reaches done in an eval — nobody approves.
	if c := m.Rows[1].Cells[0]; c.Pass || c.Status != "awaiting_approval" {
		t.Errorf("expects-done passed or wrong status: %+v", c)
	}
	// An unknown backend is a failed column, not a crashed matrix.
	if c := m.Rows[0].Cells[2]; c.Pass || c.Status != "error" || c.Error == "" {
		t.Errorf("missing provider: %+v", c)
	}
	if tot := m.Totals[0]; tot.Passed != 1 || tot.Cases != 2 || tot.PassRate != 0.5 || !tot.CostKnown {
		t.Errorf("mock totals: %+v", tot)
	}
	if tot := m.Totals[2]; tot.Passed != 0 || tot.CostKnown {
		t.Errorf("missing totals: %+v", tot)
	}
}
