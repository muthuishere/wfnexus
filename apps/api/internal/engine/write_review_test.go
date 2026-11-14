package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
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

// The pin covers the values, not only the text: the run text holds the same
// references whatever the input is, so an input changed between the pause and
// the approval must still stop the step.
func TestAnApprovedWriteCallWhoseInputChangedDoesNotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\ninput_schema: {type: object, properties: {title: {type: string}}}\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: \"{{ .Input.title }}\", repo: o/r, responsePolicy: auto }\n",
	})
	h := newHarness(t, defs["w"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	run := h.run(map[string]any{"title": "Approved title"})
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if err := h.store.UpdateRunInput(context.Background(), run.ID, []byte(`{"title":"A different title"}`)); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Approve(context.Background(), run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "failed" || !strings.Contains(run.Error, "changed after it was approved") {
		t.Fatalf("run = %s (%s), want failed: changed after it was approved", run.Status, run.Error)
	}
	if b, _ := os.ReadFile(calls); len(b) > 0 {
		t.Fatalf("gh ran with an input that changed after approval: %s", b)
	}
}

// responsePolicy: approval is the engine's pause: a gh write with it pauses
// exactly once, then writes; draft still writes nothing after approval.
func TestApprovalPolicyIsTheEnginesSinglePause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	for policy, wantWrite := range map[string]bool{"approval": true, "draft": false} {
		t.Run(policy, func(t *testing.T) {
			calls := ghStub(t)
			defs := loadWithModules(t, map[string]string{
				"w.yaml": "name: w\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: " + policy + " }\n",
			})
			h := newHarness(t, defs["w"], newFakeLLM(t), "")
			h.eng.cfg.ModulesDir = repoPath(t, "modules")
			run := h.run(nil)
			if run.Status != "awaiting_approval" {
				t.Fatalf("run = %s", run.Status)
			}
			if err := h.eng.Approve(context.Background(), run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
				t.Fatal(err)
			}
			run = h.wait(run.ID)
			if run.Status != "done" {
				t.Fatalf("after one approval the run = %s (%s), want done: no second pause", run.Status, run.Error)
			}
			b, _ := os.ReadFile(calls)
			if wrote := strings.Contains(string(b), "issue create"); wrote != wantWrite {
				t.Fatalf("policy %s: wrote = %v, want %v (%s)", policy, wrote, wantWrite, b)
			}
		})
	}
}

// That single pause keeps the workflow's approver policy: where the workflow
// says who may approve, whoever started the run may not approve its write.
func TestTheWritePauseRefusesSelfApprovalUnderAPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: review\n    run: echo ok\n    approvers: [ada, bob]\n  - id: open\n    needs: [review]\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: approval }\n",
	})
	h := newHarness(t, defs["w"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	run, err := h.store.CreateRun(context.Background(), "local", "w", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetTriggeredBy(context.Background(), run.ID, "ada"); err != nil {
		t.Fatal(err)
	}
	h.runs = append(h.runs, run.ID)
	h.eng.Start(run.ID)
	got := h.wait(run.ID)
	if got.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", got.Status, got.Error)
	}
	err = h.eng.Approve(context.Background(), run.ID, got.CurrentStep, Actor{ID: "ada", Via: "api", Authenticated: true, Role: "admin"})
	if err == nil || !strings.Contains(err.Error(), "started this run") {
		t.Fatalf("the starter approving their own write returned %v", err)
	}
	if err := h.eng.Approve(context.Background(), run.ID, got.CurrentStep, Actor{ID: "bob", Via: "api", Authenticated: true, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if done := h.wait(run.ID); done.Status != "done" {
		t.Fatalf("after bob approved: %s (%s)", done.Status, done.Error)
	}
	if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "issue create") {
		t.Fatalf("the approved write did not happen: %s", b)
	}
}

// The pin covers the environment the call runs with: an env-store value
// changed between the pause and the approval (here GH_HOST, where gh would
// send the write) stops the step.
func TestAnApprovedWriteCallWhoseEnvChangedDoesNotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	t.Setenv("WFX_SECRET_KEY", "dGVzdC1rZXktMzItYnl0ZXMtZXhhY3RseS0wMDAwMDA=")
	calls := ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: auto }\n",
	})
	h := newHarness(t, defs["w"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	ctx := context.Background()
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", "GH_HOST", "github.com", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.eng.DeleteEnvVar(ctx, store.ScopeSystem, "", "GH_HOST") })
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", "GH_HOST", "gh.elsewhere.example", false); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "failed" || !strings.Contains(run.Error, "changed after it was approved") {
		t.Fatalf("run = %s (%s), want failed: changed after it was approved", run.Status, run.Error)
	}
	if b, _ := os.ReadFile(calls); len(b) > 0 {
		t.Fatalf("gh ran with an environment that changed after approval: %s", b)
	}
}

// startAs starts a run of the harness's workflow as triggeredBy and waits.
func startAs(t *testing.T, h *harness, workflowName, triggeredBy string) *store.Run {
	t.Helper()
	run, err := h.store.CreateRun(context.Background(), "local", workflowName, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetTriggeredBy(context.Background(), run.ID, triggeredBy); err != nil {
		t.Fatal(err)
	}
	h.runs = append(h.runs, run.ID)
	h.eng.Start(run.ID)
	return h.wait(run.ID)
}

// A run started by another run belongs to the person who started the top run:
// that person may not approve the child's write either.
func TestANestedRunsWriteCannotBeApprovedByTheRootStarter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: review\n    run: echo ok\n    approvers: [ada, bob]\n  - id: open\n    needs: [review]\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: auto }\n",
	})
	h := newHarness(t, defs["w"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	parent, err := h.store.CreateRun(context.Background(), "local", "w", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetTriggeredBy(context.Background(), parent.ID, "ada"); err != nil {
		t.Fatal(err)
	}
	child := startAs(t, h, "w", "run:"+parent.ID.String())
	if child.Status != "awaiting_approval" {
		t.Fatalf("child = %s (%s)", child.Status, child.Error)
	}
	err = h.eng.Approve(context.Background(), child.ID, child.CurrentStep, Actor{ID: "ada", Via: "api", Authenticated: true})
	if err == nil || !strings.Contains(err.Error(), "started this run") {
		t.Fatalf("the root starter approving the child's write returned %v", err)
	}
}

// The policy a write's pause is answered under is the one pinned when it
// paused: a definition reloaded with a looser policy does not change who may
// answer. And a write's pause has no exemption for an unnamed answer.
func TestAWritePauseIsAnsweredUnderThePinnedPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: review\n    run: echo ok\n    approvers: [ada, bob]\n  - id: open\n    needs: [review]\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: auto }\n",
	})
	def := defs["w"]
	h := newHarness(t, def, newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	run := startAs(t, h, "w", "ada")
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	// "Reload" a definition that drops the policy.
	for i := range def.Steps {
		def.Steps[i].PreventSelfApproval = false
		def.Steps[i].Approvers = nil
	}
	err := h.eng.Approve(context.Background(), run.ID, run.CurrentStep, Actor{ID: "ada", Via: "api", Authenticated: true})
	if err == nil || !strings.Contains(err.Error(), "started this run") {
		t.Fatalf("a reloaded definition loosened who may approve: %v", err)
	}
	err = h.eng.Approve(context.Background(), run.ID, run.CurrentStep, Actor{Via: "loopback"})
	if err == nil {
		t.Fatal("an unnamed answer approved a write")
	}
}

// The same rule at dispatch, for a definition the loader never saw: an
// approved write step placed on a worker fails before a job is queued for it.
func TestAWriteStepIsNotDispatchedToAWorker(t *testing.T) {
	def := &workflow.Definition{Name: "placed-write", Steps: []workflow.Step{{
		ID: "send.act", Run: "echo would write", RunsOn: "box-elsewhere",
		WriteEffect: true, WriteEntry: true, RequiresApproval: true,
	}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if err := h.eng.Approve(context.Background(), run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "failed" || !strings.Contains(run.Error, "write commands run on the server's runner only for now") {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
}
