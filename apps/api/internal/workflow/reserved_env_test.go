package workflow

import (
	"path/filepath"
	"strings"
	"testing"
)

// WFX_* belongs to the platform and the env store: a workflow env or a step
// env that sets one is refused at load, naming where and which variable.
func TestAWorkflowMayNotSetAReservedVariable(t *testing.T) {
	for name, c := range map[string]struct{ yaml, where, varName string }{
		"workflow env": {"name: r\nenv: { WFX_STEP_ID: forged }\nsteps:\n  - id: s\n    run: echo hi\n", "r", "WFX_STEP_ID"},
		"step env":     {"name: r\nsteps:\n  - id: s\n    env: { WFX_CONVERSATION_URL: https://elsewhere.example }\n    run: echo hi\n", "r/s", "WFX_CONVERSATION_URL"},
		"lower case":   {"name: r\nsteps:\n  - id: s\n    env: { wfx_run_id: x }\n    run: echo hi\n", "r/s", "wfx_run_id"},
		"job env":      {"name: r\njobs:\n  j:\n    env: { WFX_DATA_1: x }\n    steps:\n      - id: s\n        run: echo hi\n", "r/j.s", "WFX_DATA_1"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "r.yaml"), c.yaml)
			_, err := LoadDir(dir, catalog())
			if err == nil || !strings.Contains(err.Error(), c.where+":") || !strings.Contains(err.Error(), c.varName) || !strings.Contains(err.Error(), "WFX_* variables are provided by the platform") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ok.yaml"), "name: ok\nenv: { MY_WFX_THING: fine }\nsteps:\n  - id: s\n    run: echo hi\n")
	if _, err := LoadDir(dir, catalog()); err != nil {
		t.Fatalf("a name that merely contains WFX_ was refused: %v", err)
	}
}
