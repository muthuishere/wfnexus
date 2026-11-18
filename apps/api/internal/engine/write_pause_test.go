package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// fakeCLIs puts every CLI a shipped module calls on PATH as a stub that
// records its call, so nothing real runs.
func fakeCLIs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	for _, cli := range []string{"gh", "apl", "aos", "ctx-optimize", "tmvp", "node", "python3", "chrome-agent", "xbrowser"} {
		body := fmt.Sprintf("#!/bin/sh\necho \"%s $*\" >> %s\necho '{}'\n", cli, calls)
		if err := os.WriteFile(filepath.Join(dir, cli), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// Every shipped module command marked `effect: write`, used from a workflow,
// pauses the run for approval before any of its steps has run.
func TestEveryWriteCommandPausesBeforeAnythingRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := fakeCLIs(t)
	mods, errs := workflow.FindModules("builtin", repoPath(t, "modules"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var writes []string
	for _, m := range mods {
		for _, name := range m.CommandNames() {
			if task, _ := m.Callable(name); task.Effect == "write" {
				writes = append(writes, name)
			}
		}
	}
	sort.Strings(writes)
	// Write commands whose steps run on a worker (runs-on the machine named in
	// their input) are refused at load until workers check a call's files.
	onWorker := map[string]bool{"aos.file-write": true, "aos.shell": true}
	if len(writes) == 0 {
		t.Fatal("no shipped write commands found")
	}
	for _, name := range writes {
		t.Run(name, func(t *testing.T) {
			var mod *workflow.Module
			for _, m := range mods {
				if strings.HasPrefix(name, m.Name+".") {
					mod = m
				}
			}
			task, _ := mod.Callable(name)
			with := map[string]string{}
			for k := range task.Inputs {
				with[k] = "x"
			}
			wj, _ := json.Marshal(with)
			dir := t.TempDir()
			major := strings.TrimPrefix(strings.SplitN(mod.Version, ".", 2)[0], "v")
			if err := os.WriteFile(filepath.Join(dir, "wf.yaml"), []byte(fmt.Sprintf(
				"name: write-call\nsteps:\n  - id: call\n    uses: %s@%s\n    with: %s\n", name, major, wj)), 0o644); err != nil {
				t.Fatal(err)
			}
			defs, err := workflow.LoadDirWithTasks(dir, "", nil, workflow.WithModules(mods))
			if onWorker[name] {
				// Its steps run on a worker, where a write call may not run yet.
				if err == nil || !strings.Contains(err.Error(), "write commands run on the server's runner only for now") {
					t.Fatalf("a write command on a worker loaded: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			def := defs["write-call"]
			if !def.Steps[0].RequiresApproval || !def.Steps[0].WriteEntry {
				t.Fatalf("the command's first step does not pause: %+v", def.Steps[0])
			}
			_ = os.Remove(calls)
			h := newHarness(t, def, newFakeLLM(t), "")
			run := h.run(nil)
			if run.Status != "awaiting_approval" {
				t.Fatalf("run = %s (%s), want awaiting_approval", run.Status, run.Error)
			}
			steps, _ := h.store.ListSteps(context.Background(), run.ID)
			for _, s := range steps {
				if len(s.Output) > 0 && string(s.Output) != "null" {
					t.Fatalf("step %s ran before approval: %s", s.StepID, s.Output)
				}
			}
			if b, err := os.ReadFile(calls); err == nil && len(b) > 0 {
				t.Fatalf("a CLI ran before approval: %s", b)
			}
		})
	}
}

// After approval a write command still follows its own response policy: gh's
// default is draft, so nothing is written.
func TestAnApprovedWriteCommandKeepsItsResponsePolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := fakeCLIs(t)
	// gh's planner is Python; use the real interpreter for it.
	t.Setenv("PATH", strings.Replace(os.Getenv("PATH"), filepath.Dir(calls)+string(os.PathListSeparator), "", 1))
	bin := t.TempDir()
	stub := fmt.Sprintf("#!/bin/sh\necho \"gh $*\" >> %s\necho '{}'\n", calls)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	mods, _ := workflow.FindModules("builtin", repoPath(t, "modules"))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wf.yaml"), []byte(
		"name: gh-draft\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defs, err := workflow.LoadDirWithTasks(dir, "", nil, workflow.WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, defs["gh-draft"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s, want awaiting_approval", run.Status)
	}
	first := defs["gh-draft"].Steps[0].ID
	if err := h.eng.Approve(context.Background(), run.ID, first, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status == "awaiting_approval" {
		t.Fatalf("approval did not continue the run")
	}
	if b, _ := os.ReadFile(calls); strings.Contains(string(b), "issue create") {
		t.Fatalf("draft policy still wrote: %s", b)
	}
}
