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

// A write call whose `when:` is false when its first step comes up is skipped
// whole, even if a parallel author step makes the condition true before the
// call's later steps, and plants input files for them. Before the fix the
// call's first step was skipped, and its write step then ran unapproved.
func TestASkippedCallStaysSkippedWhenItsConditionFlips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	for _, c := range []struct{ name, uses, with, plant string }{
		{"gh", "gh.issue-create@0", "{ title: t, repo: o/r }",
			"printf 'planted\\n' > in.title; printf 'o/r\\n' > in.repo; printf 'auto\\n' > in.responsePolicy"},
		{"apl", "apl.whatsapp-send@0", "{ chat: g1@g.us, text: hi }",
			`printf '{"chat":"g1@g.us","text":"planted","policy":"auto","key":"k","profile":"","replyTo":""}' > plan.json`},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := ghStub(t)
			bin := filepath.Dir(calls)
			// apl's stub records too; nothing may reach either.
			if err := os.WriteFile(filepath.Join(bin, "apl"), []byte(fmt.Sprintf("#!/bin/sh\necho \"apl $*\" >> %s\necho '{}'\n", calls)), 0o755); err != nil {
				t.Fatal(err)
			}
			work := t.TempDir()
			// flip runs beside the call: slow, then true, then it plants the
			// call's input files where the call's steps run.
			flip := fmt.Sprintf(`sleep 1; d="%s/$WFX_RUN_ID/calls/send"; mkdir -p "$d"; cd "$d" && %s; echo ok`, work, c.plant)
			wf := fmt.Sprintf("name: flipped\nsteps:\n  - id: flip\n    run: %q\n  - id: send\n    uses: %s\n    with: %s\n    when: [{ path: flip.ok, equals: true }]\n  - id: after\n    needs: [flip, send]\n    run: echo after\n", flip, c.uses, c.with)
			defs := loadWithModules(t, map[string]string{"w.yaml": wf})
			h := newHarness(t, defs["flipped"], newFakeLLM(t), "")
			h.eng.cfg.ModulesDir = repoPath(t, "modules")
			h.eng.cfg.WorkDir = work
			run := h.run(nil)
			if run.Status == "awaiting_approval" {
				t.Fatalf("the call paused although its condition was false when it came up")
			}
			steps, _ := h.store.ListSteps(context.Background(), run.ID)
			for _, s := range steps {
				if strings.HasPrefix(s.StepID, "send.") && s.Status != "skipped" {
					t.Errorf("call step %s is %s, want skipped", s.StepID, s.Status)
				}
			}
			if b, _ := os.ReadFile(calls); len(b) > 0 {
				t.Fatalf("the write ran unapproved: %s", b)
			}
		})
	}
}

// A file the call's approved first step wrote (in.title for gh, plan.json for
// apl) that is changed before the write step makes that step fail: what runs
// is what was approved.
func TestAChangedCallFileStopsTheWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	for _, c := range []struct{ name, uses, with, file string }{
		{"gh", "gh.issue-create@0", "{ title: approved, repo: o/r, responsePolicy: auto }", "in.title"},
		{"apl", "apl.whatsapp-send@0", "{ chat: g1@g.us, text: approved, responsePolicy: auto }", "plan.json"},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := ghStub(t)
			defs := loadWithModules(t, map[string]string{
				"w.yaml": "name: w\nsteps:\n  - id: send\n    uses: " + c.uses + "\n    with: " + c.with + "\n",
			})
			h := newHarness(t, defs["w"], newFakeLLM(t), "")
			h.eng.cfg.ModulesDir = repoPath(t, "modules")
			h.eng.testHookBeforeCallStep = func(step *workflow.Step, dir string) {
				if !step.WriteEntry && step.WriteEffect {
					_ = os.WriteFile(filepath.Join(dir, c.file), []byte("tampered\n"), 0o600)
				}
			}
			run := h.run(nil)
			if run.Status != "awaiting_approval" {
				t.Fatalf("run = %s (%s)", run.Status, run.Error)
			}
			if err := h.eng.Approve(context.Background(), run.ID, run.CurrentStep, Actor{ID: "alice", Via: "test"}); err != nil {
				t.Fatal(err)
			}
			run = h.wait(run.ID)
			if run.Status != "failed" || !strings.Contains(run.Error, "changed after it was approved") {
				t.Fatalf("run = %s (%s), want failed: changed after it was approved", run.Status, run.Error)
			}
			if b, _ := os.ReadFile(calls); len(b) > 0 {
				t.Fatalf("the write ran on tampered files: %s", b)
			}
		})
	}
}
