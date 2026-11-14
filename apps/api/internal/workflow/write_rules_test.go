package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModule is a module with a write command (plan, a module approval step,
// act) and a read command.
func writeModule(t *testing.T) []*Module {
	t.Helper()
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
	return mods
}

func approvals(d *Definition) map[string]bool {
	got := map[string]bool{}
	for _, s := range d.Steps {
		got[s.ID] = s.RequiresApproval
	}
	return got
}

// A write call reached through another workflow keeps its pause: an override
// that would switch it off is refused at load, and any other override leaves
// it in place.
func TestAWriteCallKeepsItsPauseThroughNestedWorkflows(t *testing.T) {
	mods := writeModule(t)
	inner := "name: inner\non: workflow_call\nsteps:\n  - id: s\n    uses: m.send@1\n"
	t.Run("override to false is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "inner.yaml"), inner)
		write(t, filepath.Join(dir, "outer.yaml"), "name: outer\nuses:\n  - use: inner\n    with: { \"*\": { requires_approval: false } }\nsteps:\n  - id: after\n    run: echo done\n")
		_, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
		if err == nil || !strings.Contains(err.Error(), "requires_approval: false") || !strings.Contains(err.Error(), "cannot be switched off") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unrelated override keeps the pause", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "inner.yaml"), inner)
		write(t, filepath.Join(dir, "outer.yaml"), "name: outer\nuses:\n  - use: inner\n    with: { \"*\": { model: tiny } }\nsteps:\n  - id: after\n    run: echo done\n")
		defs, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
		if err != nil {
			t.Fatal(err)
		}
		got := approvals(defs["outer"])
		if !got["inner.s.plan"] {
			t.Fatalf("the write call lost its pause through the outer workflow: %v", got)
		}
	})
	t.Run("a step-level module call cannot be told not to pause", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "wf.yaml"), "name: wf\nuses:\n  - use: m.send@1\n    as: s\n    with: { plan: { requires_approval: false } }\n")
		_, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
		if err == nil || !strings.Contains(err.Error(), "cannot be switched off") {
			t.Fatalf("err = %v", err)
		}
	})
}

// The first step of a write call pauses; a step the module itself marks
// requires_approval keeps it; a read call has none.
func TestAWriteCallPausesFirstAndKeepsTheModulesOwnApproval(t *testing.T) {
	mods := writeModule(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "wf.yaml"), "name: wf\nsteps:\n  - id: s\n    uses: m.send@1\n  - id: r\n    uses: m.read@1\n")
	defs, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	got := approvals(defs["wf"])
	want := map[string]bool{"s.plan": true, "s.ask": true, "s.act": false, "r.get": false}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s requires_approval = %v, want %v (all: %v)", id, got[id], w, got)
		}
	}
}

// A write command cannot be used in a planned workflow, whose order comes
// from facts rather than from the command's own needs.
func TestAWriteCallIsRefusedInAPlannedWorkflow(t *testing.T) {
	mods := writeModule(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "g.yaml"), "name: g\ngoal: s.act\nsteps:\n  - id: s\n    uses: m.send@1\n")
	_, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err == nil || !strings.Contains(err.Error(), "g/s.") || !strings.Contains(err.Error(), "planned workflow") {
		t.Fatalf("err = %v", err)
	}
	// A read command is fine there.
	write(t, filepath.Join(dir, "g.yaml"), "name: g\ngoal: r.get\nsteps:\n  - id: r\n    uses: m.read@1\n")
	if _, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods)); err != nil {
		t.Fatalf("a read command in a planned workflow: %v", err)
	}
}

// A `uses:` step cannot loop: foreach on it is a load error, not ignored.
func TestAUsesStepCannotLoop(t *testing.T) {
	mods := writeModule(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "wf.yaml"), "name: wf\nsteps:\n  - id: s\n    uses: m.send@1\n    foreach: [a, b]\n")
	_, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err == nil || !strings.Contains(err.Error(), "cannot loop") {
		t.Fatalf("err = %v", err)
	}
}

// Where the workflow has an approvers policy, a write's pause also refuses
// self-approval; with no policy, the starter may approve.
func TestAWritePauseFollowsTheWorkflowsApproverPolicy(t *testing.T) {
	mods := writeModule(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.yaml"), "name: a\nsteps:\n  - id: s\n    uses: m.send@1\n")
	write(t, filepath.Join(dir, "b.yaml"), "name: b\nsteps:\n  - id: review\n    run: echo r\n    requires_approval: true\n    approvers: [role:lead]\n  - id: s\n    needs: [review]\n    uses: m.send@1\n")
	defs, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range defs["a"].Steps {
		if s.ID == "s.plan" && s.PreventSelfApproval {
			t.Fatal("no policy, yet self-approval is refused")
		}
	}
	for _, s := range defs["b"].Steps {
		if s.ID == "s.plan" && !s.PreventSelfApproval {
			t.Fatal("the workflow has an approvers policy, yet the write may be self-approved")
		}
	}
}

// placedWriteModule is a write module whose steps run where runsOn says.
func placedWriteModule(t *testing.T, runsOn string) []*Module {
	t.Helper()
	reg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reg, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(reg, "p", "module.yaml"), fmt.Sprintf(`
name: p
version: 1.0.0
commands:
  send:
    effect: write
    inputs:
      machine: { required: true }
    steps:
      - { id: act, run: echo act, runs-on: %q }
`, runsOn))
	mods, errs := FindModules("reg", reg)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return mods
}

// A write call whose steps would run on a worker is refused at load, however
// the worker is named (the workflow, a job, the module's own steps, a
// template); one on a label the server serves itself loads.
func TestAWriteCallOnAWorkerIsRefusedAtLoad(t *testing.T) {
	const call = "    uses: %s@1\n    with: { machine: x }\n"
	for name, c := range map[string]struct {
		mods []*Module
		wf   string
	}{
		"workflow runs-on": {writeModule(t), "name: w\nruns-on: build-box\nsteps:\n  - id: s\n    uses: m.send@1\n"},
		"job runs-on":      {writeModule(t), "name: w\njobs:\n  j:\n    runs-on: build-box\n    steps:\n      - id: s\n        uses: m.send@1\n"},
		"module step":      {placedWriteModule(t, "build-box"), "name: w\nsteps:\n  - id: s\n" + fmt.Sprintf(call, "p.send")},
		"templated":        {placedWriteModule(t, "{{ .Input.machine }}"), "name: w\nsteps:\n  - id: s\n" + fmt.Sprintf(call, "p.send")},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "w.yaml"), c.wf)
			_, err := LoadDirWithTasks(dir, "", catalog(), WithModules(c.mods), WithLocalLabels([]string{"local"}))
			if err == nil || !strings.Contains(err.Error(), "write commands run on the server's runner only for now") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	dir := t.TempDir()
	mods := append(placedWriteModule(t, "server-box"), writeModule(t)...)
	write(t, filepath.Join(dir, "w.yaml"), "name: w\nsteps:\n  - id: s\n"+fmt.Sprintf(call, "p.send")+"  - id: r\n    uses: m.read@1\n")
	if _, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods), WithLocalLabels([]string{"server-box"})); err != nil {
		t.Fatalf("a write on a label the server serves should load: %v", err)
	}
	write(t, filepath.Join(dir, "w.yaml"), "name: w\nruns-on: build-box\nsteps:\n  - id: r\n    uses: m.read@1\n")
	if _, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods), WithLocalLabels([]string{"server-box"})); err != nil {
		t.Fatalf("a read on a worker should load: %v", err)
	}
}
