package workflow

import (
	"os"
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

// A module command marked effect: write gets exactly one approval, on its
// first step; a command that writes nothing gets none.
func TestAWriteCommandGetsOneApprovalOnItsFirstStep(t *testing.T) {
	reg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reg, "m"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(reg, "m", "module.yaml"), `
name: m
version: 1.0.0
commands:
  send:
    effect: write
    steps:
      - { id: plan, run: echo plan }
      - { id: ask, needs: [plan], requires_approval: true, run: echo ask }
      - { id: act, needs: [ask], run: echo act }
  read:
    steps:
      - { id: get, run: echo get }
`)
	mods, errs := FindModules("reg", reg)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "wf.yaml"), "name: wf\nsteps:\n  - id: s\n    uses: m.send@1\n  - id: r\n    uses: m.read@1\n")
	defs, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range defs["wf"].Steps {
		got[s.ID] = s.RequiresApproval
	}
	want := map[string]bool{"s.plan": true, "s.ask": false, "s.act": false, "r.get": false}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s requires_approval = %v, want %v (all: %v)", id, got[id], w, got)
		}
	}
}
