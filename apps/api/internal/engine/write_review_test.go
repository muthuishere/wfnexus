package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// ghStub puts a recording `gh` first on PATH (python3 stays the real one, for
// the gh module's own script) and returns the path of its call log.
func ghStub(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls.log")
	stub := fmt.Sprintf("#!/bin/sh\necho \"gh $*\" >> %s\necho 'https://github.com/o/r/issues/1'\n", calls)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func loadWithModules(t *testing.T, files map[string]string) map[string]*workflow.Definition {
	t.Helper()
	mods, errs := workflow.FindModules("builtin", repoPath(t, "modules"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	defs, err := workflow.LoadDirWithTasks(dir, "", nil, workflow.WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

// A write call reached through another workflow, with an override on it,
// still pauses: the stub gh records nothing until a person approves, and the
// pause shows what will run.
func TestANestedWriteCallRecordsNothingUntilApproved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"inner.yaml": "name: inner\non: workflow_call\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: \"Disk full on db-2\", repo: o/r, responsePolicy: auto }\n",
		"outer.yaml": "name: outer\nuses:\n  - use: inner\n    with: { \"*\": { model: tiny } }\n",
	})
	h := newHarness(t, defs["outer"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s), want awaiting_approval", run.Status, run.Error)
	}
	if b, _ := os.ReadFile(calls); len(b) > 0 {
		t.Fatalf("gh ran before approval: %s", b)
	}
	st, _ := h.store.GetStep(context.Background(), run.ID, run.CurrentStep)
	if !strings.Contains(st.Prompt, "Disk full on db-2") {
		t.Fatalf("the pause does not show what will run: %q", st.Prompt)
	}
	if err := h.eng.Approve(context.Background(), run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "issue create") {
		t.Fatalf("after approval gh was not called (run %s %s): %s", run.Status, run.Error, b)
	}
}

// What was approved is what runs: if the call's text changes between the
// pause and the approval (the workflow edited and reloaded), the step fails
// instead of running the new text.
func TestAnApprovedWriteCallThatChangedDoesNotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: \"Approved title\", repo: o/r, responsePolicy: auto }\n",
	})
	def := defs["w"]
	h := newHarness(t, def, newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s", run.Status)
	}
	// The definition is edited while the run waits.
	for i := range def.Steps {
		def.Steps[i].Run = strings.ReplaceAll(def.Steps[i].Run, "Approved title", "Something else")
		for k, v := range def.Steps[i].RunConst {
			def.Steps[i].RunConst[k] = strings.ReplaceAll(v, "Approved title", "Something else")
		}
	}
	if err := h.eng.Approve(context.Background(), run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "failed" || !strings.Contains(run.Error, "changed after it was approved") {
		t.Fatalf("run = %s (%s), want failed: changed after it was approved", run.Status, run.Error)
	}
	if b, _ := os.ReadFile(calls); len(b) > 0 {
		t.Fatalf("gh ran a call that changed after approval: %s", b)
	}
}

// Two module calls running at once each get their own directory for in.*
// files: the stub gh records the directory it ran in and the repo it read.
func TestModuleCallsDoNotShareTheirInputFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls.log")
	stub := fmt.Sprintf("#!/bin/sh\necho \"$(pwd) $*\" >> %s\necho '[]'\n", calls)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defs := loadWithModules(t, map[string]string{
		"two.yaml": "name: two\nsteps:\n  - id: a\n    uses: gh.pr-list@0\n    with: { repo: o/first }\n  - id: b\n    uses: gh.pr-list@0\n    with: { repo: o/second }\n  - id: c\n    needs: [a, b]\n    run: echo both\n",
	})
	h := newHarness(t, defs["two"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	raw, _ := os.ReadFile(calls)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	dirs := map[string]string{}
	for _, l := range lines {
		for call, repo := range map[string]string{"a": "o/first", "b": "o/second"} {
			if strings.Contains(l, "-R "+repo) {
				dirs[call] = strings.Fields(l)[0]
			}
		}
	}
	if !strings.HasSuffix(dirs["a"], filepath.Join("calls", "a")) || !strings.HasSuffix(dirs["b"], filepath.Join("calls", "b")) {
		t.Fatalf("calls did not each run in their own directory: %v (log %q)", dirs, raw)
	}
}
