package engine

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A retry of a write call's later step starts the call again from its first
// step, which pauses again: a write changed after its approval (the workflow
// reloaded while the run waited further on) never runs on the old approval.
func TestARetryOfAWriteStepPausesTheCallAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: auto }\n  - id: later\n    needs: [open]\n    requires_approval: true\n    run: echo later\n",
	})
	def := defs["w"]
	h := newHarness(t, def, newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	ctx := context.Background()
	run := h.run(nil)
	run = waitSettled(t, h, run.ID)
	if run.Status != "awaiting_approval" || run.CurrentStep != "open.plan" {
		t.Fatalf("run = %s at %s", run.Status, run.CurrentStep)
	}
	if err := h.eng.Approve(ctx, run.ID, "open.plan", Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = waitSettled(t, h, run.ID)
	if run.Status != "awaiting_approval" || run.CurrentStep != "later" {
		t.Fatalf("run = %s at %s (%s), want parked at later", run.Status, run.CurrentStep, run.Error)
	}
	if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "issue create") {
		t.Fatalf("the approved write did not run: %s", b)
	}
	_ = os.Remove(calls)
	// The definition is reloaded with a different write while the run waits.
	for i := range def.Steps {
		if def.Steps[i].ID == "open.act" {
			def.Steps[i].Run = `gh SWAPPED-COMMAND`
		}
	}
	if err := h.eng.Retry(ctx, run.ID, "open.act"); err != nil {
		t.Fatal(err)
	}
	run = waitSettled(t, h, run.ID)
	if run.Status != "awaiting_approval" || run.CurrentStep != "open.plan" {
		t.Fatalf("after the retry the run = %s at %s (%s), want a new pause at open.plan", run.Status, run.CurrentStep, run.Error)
	}
	if b, _ := os.ReadFile(calls); len(b) > 0 {
		t.Fatalf("a write ran on the old approval: %s", b)
	}
	st, _ := h.store.GetStep(ctx, run.ID, "open.plan")
	if !strings.Contains(st.Prompt, "SWAPPED-COMMAND") {
		t.Fatalf("the new pause does not show the changed write: %q", st.Prompt)
	}
}

// The profile a write call targets is resolved at the pause and shown; the
// write uses that profile, and a profile changed after the approval stops it.
func TestAWriteCallUsesTheProfileItWasApprovedWith(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "changed after approval"}[changed], func(t *testing.T) {
			calls := ghStub(t)
			defs := loadWithModules(t, map[string]string{
				"w.yaml": "name: w\nsteps:\n  - id: open\n    uses: gh.issue-create@0\n    with: { title: t, responsePolicy: auto }\n",
			})
			h := newHarness(t, defs["w"], newFakeLLM(t), "")
			h.eng.cfg.ModulesDir = repoPath(t, "modules")
			ctx := context.Background()
			if _, err := h.eng.SetProfile(ctx, "gh", Profile{Name: "work", Account: "me", Inputs: map[string]string{"repo": "o/approved"}, Default: true}); err != nil {
				t.Fatal(err)
			}
			run := waitSettled(t, h, h.run(nil).ID)
			if run.Status != "awaiting_approval" {
				t.Fatalf("run = %s (%s)", run.Status, run.Error)
			}
			st, _ := h.store.GetStep(ctx, run.ID, run.CurrentStep)
			if !strings.Contains(st.Prompt, "profile work") || !strings.Contains(st.Prompt, "repo=o/approved") {
				t.Fatalf("the pause does not show the resolved profile: %q", st.Prompt)
			}
			if changed {
				// After the approval, while the call's first step runs, the
				// default profile is pointed elsewhere.
				h.eng.testHookBeforeCallStep = func(step *workflow.Step, _ string) {
					if step.ID == "open.plan" {
						if _, err := h.eng.SetProfile(ctx, "gh", Profile{Name: "other", Account: "x", Inputs: map[string]string{"repo": "o/elsewhere"}, Default: true}); err != nil {
							t.Error(err)
						}
					}
				}
			}
			if err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
				t.Fatal(err)
			}
			run = h.wait(run.ID)
			b, _ := os.ReadFile(calls)
			if !changed {
				if run.Status != "done" || !strings.Contains(string(b), "-R o/approved") {
					t.Fatalf("run = %s (%s); gh: %s", run.Status, run.Error, b)
				}
				return
			}
			if run.Status != "failed" || !strings.Contains(run.Error, "changed after it was approved") {
				t.Fatalf("run = %s (%s), want failed: changed after it was approved", run.Status, run.Error)
			}
			if len(b) > 0 {
				t.Fatalf("gh ran after the profile changed: %s", b)
			}
		})
	}
}

// Every step of a write call is checked, not only the first: an env-store
// value changed between the call's steps (GH_HOST, where gh sends the write)
// stops the write step.
func TestAnEnvChangeBetweenACallsStepsStopsTheWrite(t *testing.T) {
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
	run := waitSettled(t, h, h.run(nil).ID)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	h.eng.testHookBeforeCallStep = func(step *workflow.Step, _ string) {
		if step.ID == "open.plan" {
			if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", "GH_HOST", "gh.elsewhere.example", false); err != nil {
				t.Error(err)
			}
		}
	}
	if err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "failed" || run.CurrentStep != "open.act" || !strings.Contains(run.Error, "changed after it was approved") {
		t.Fatalf("run = %s at %s (%s), want failed at open.act: changed after it was approved", run.Status, run.CurrentStep, run.Error)
	}
	if b, _ := os.ReadFile(calls); len(b) > 0 {
		t.Fatalf("gh ran after GH_HOST changed: %s", b)
	}
}

// A write step runs only after its call's first step was approved AND
// finished (callgate.go, followCallHead). Each case is a first step's record
// that is one but not the other.
func TestAWriteStepNeedsItsCallsFirstStepApprovedAndDone(t *testing.T) {
	def := &workflow.Definition{Name: "gated", Steps: []workflow.Step{
		{ID: "send.plan", Run: "echo plan", Module: "m",
			WriteEffect: true, WriteEntry: true, RequiresApproval: true, CallHead: "send.plan", CallSelf: "send.plan"},
		{ID: "send.act", Run: "echo act", Needs: []string{"send.plan"}, Module: "m",
			WriteEffect: true, CallHead: "send.plan", CallSelf: "send.act"},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	ctx := context.Background()
	for _, c := range []struct {
		name, status, resolution, want string
	}{
		{"approved, not started", "approved", "approved", "not done"},
		{"approved, still running", "running", "approved", "not done"},
		{"waiting for approval", "awaiting_approval", "", "not done"},
		{"done without an approval", "done", "", "not approved"},
		{"done, rejected", "done", "rejected", "not approved"},
		{"approved and done", "done", "approved", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			run, err := h.store.CreateRun(ctx, "local", "gated", []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			for i, s := range def.Steps {
				if err := h.store.EnsureStep(ctx, run.ID, s.ID, i); err != nil {
					t.Fatal(err)
				}
			}
			h.eng.setStep(ctx, run.ID, "send.plan", model.StepPatch{Status: str(c.status), Resolution: str(c.resolution)})
			_, err = h.eng.followCallHead(ctx, run.ID, &def.Steps[1])
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// Under an approver policy a write's approval needs a signed-in approver: a
// typed name on the loopback is refused, the same name signed in is not.
func TestAWriteUnderAPolicyNeedsASignedInApprover(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: review\n    run: echo ok\n    approvers: [ada, bob]\n  - id: open\n    needs: [review]\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: approval }\n",
	})
	h := newHarness(t, defs["w"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	ctx := context.Background()
	run := waitSettled(t, h, h.run(nil).ID)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "bob", Via: "cli"})
	if err == nil || !strings.Contains(err.Error(), "signed-in approver") {
		t.Fatalf("a typed name approved a write under a policy: %v", err)
	}
	if err := h.eng.Approve(ctx, run.ID, run.CurrentStep, Actor{ID: "bob", Via: "api", Authenticated: true}); err != nil {
		t.Fatal(err)
	}
	if done := h.wait(run.ID); done.Status != "done" {
		t.Fatalf("after bob approved: %s (%s)", done.Status, done.Error)
	}
}

// A write call and a sibling approval paused side by side: approving each
// (once the run has settled) lets the run finish.
func TestAWriteCallBesideAnotherApprovalFinishes(t *testing.T) {
	t.Skip("flaky until #153/#154 are fixed: a sibling step starting after a pause overwrites awaiting_approval with running, and an approval arriving while the run still executes is lost")
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	calls := ghStub(t)
	defs := loadWithModules(t, map[string]string{
		"w.yaml": "name: w\nsteps:\n  - id: start\n    run: echo start\n  - id: open\n    needs: [start]\n    uses: gh.issue-create@0\n    with: { title: t, repo: o/r, responsePolicy: auto }\n  - id: ask\n    needs: [start]\n    requires_approval: true\n    run: echo ask\n",
	})
	h := newHarness(t, defs["w"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = repoPath(t, "modules")
	ctx := context.Background()
	run := waitSettled(t, h, h.run(nil).ID)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	for _, id := range []string{"open.plan", "ask"} {
		if err := h.eng.Approve(ctx, run.ID, id, Actor{ID: "alice", Via: "test"}); err != nil {
			t.Fatalf("approve %s: %v", id, err)
		}
		run = waitSettled(t, h, run.ID)
	}
	if run.Status != "done" {
		t.Fatalf("run = %s at %s (%s), want done", run.Status, run.CurrentStep, run.Error)
	}
	if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "issue create") {
		t.Fatalf("the approved write did not run: %s", b)
	}
}

// waitSettled waits until a run is no longer executing and reports it.
func waitSettled(t *testing.T, h *harness, runID uuid.UUID) *store.Run {
	t.Helper()
	if !h.eng.WaitIdle(30 * time.Second) {
		run, _ := h.store.GetRun(context.Background(), runID)
		t.Fatalf("run still executing: %s at %s", run.Status, run.CurrentStep)
	}
	return h.wait(runID)
}
