package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModulesAreFoundAndCallableFromAStep(t *testing.T) {
	reg := t.TempDir()
	for _, d := range []string{"modules/summarise", "svc", "bad"} {
		_ = os.MkdirAll(filepath.Join(reg, d), 0o755)
	}
	write(t, filepath.Join(reg, "modules", "summarise", "module.yaml"), `
name: summarise
version: 1.2.0
inputs:
  chat: { type: string, required: true }
requires:
  secrets: [SUMMARY_TOKEN]
`)
	write(t, filepath.Join(reg, "modules", "summarise", "task.yaml"), `
name: summarise
steps:
  - id: run
    run: echo summarise "{{ .Inputs.chat }}"
`)
	write(t, filepath.Join(reg, "svc", "module.yaml"), `
name: watcher
version: 1
services:
  watch: { command: wfx-wa-monitor }
commands:
  ping: { steps: [{ id: p, run: 'echo ping "{{ .Inputs.who }}"' }], inputs: { who: { type: string, required: true } } }
`)
	write(t, filepath.Join(reg, "bad", "module.yaml"), "name: bad\n")
	mods, errs := FindModules("reg", reg)
	if len(mods) != 2 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "needs a name and a version") {
		t.Fatalf("mods=%d errs=%v", len(mods), errs)
	}
	if mods[0].Name != "summarise" || mods[0].Requires.Secrets[0] != "SUMMARY_TOKEN" || mods[1].Services["watch"].Command != "wfx-wa-monitor" {
		t.Fatalf("mods = %+v", mods)
	}

	dir := t.TempDir()
	write(t, filepath.Join(dir, "wf.yaml"), `
name: wf
steps:
  - id: s
    uses: summarise@1
    with: { chat: ops }
`)
	defs, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	if got := defs["wf"].Steps[0]; got.ID != "s.run" || filled(t, got.Run, nil) != `echo summarise "ops"` {
		t.Fatalf("step = %+v", got)
	}

	write(t, filepath.Join(dir, "wf.yaml"), "name: wf\nsteps:\n  - id: s\n    uses: watcher@1\n")
	if _, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods)); err == nil || !strings.Contains(err.Error(), `unknown task "watcher"`) {
		t.Fatalf("a module with no steps of its own is not a step: err = %v", err)
	}
	// A named command is <module>.<command>, and its steps carry the module.
	write(t, filepath.Join(dir, "wf.yaml"), "name: wf\nsteps:\n  - id: s\n    uses: watcher.ping@1\n    with: { who: ops }\n")
	defs, err = LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	if got := defs["wf"].Steps[0]; got.ID != "s.p" || filled(t, got.Run, nil) != `echo ping "ops"` || got.Module != "watcher" {
		t.Fatalf("command step = %+v", got)
	}
}

func TestModuleManifestRejectsTypos(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "module.yaml"), "name: x\nversion: 1\nservicez: {}\nsteps: [{id: a, run: echo}]\n")
	if _, err := LoadModule(d); err == nil || !strings.Contains(err.Error(), "servicez") {
		t.Fatalf("err = %v", err)
	}
}
