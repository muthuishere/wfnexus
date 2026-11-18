package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// followCallHead decides a call's later step from the call's first step: a
// call's `when:` is evaluated once, on its first step, and every other step
// of the call follows that outcome.
//
//   - the first step was skipped: this step is skipped too;
//   - this step is a write command's step: it runs only after the call's
//     first step was approved AND finished; anything else fails closed.
//
// The first step's row is found relative to this step's row, so the rule
// holds inside loop iterations too. It runs on every path that starts a step
// (the scheduler, the sequential resume path, and therefore worker jobs,
// which are only queued from there).
func (e *Engine) followCallHead(ctx context.Context, runID uuid.UUID, step *workflow.Step) (skip bool, err error) {
	if step.CallHead == "" || step.CallSelf == "" || step.CallHead == step.CallSelf {
		return false, nil
	}
	if !strings.HasSuffix(step.ID, step.CallSelf) {
		if step.WriteEffect {
			return false, fmt.Errorf("step %s: cannot find the first step of its call; a write step does not run without it", step.ID)
		}
		return false, nil
	}
	headRow := strings.TrimSuffix(step.ID, step.CallSelf) + step.CallHead
	head, err := e.store.GetStep(ctx, runID, headRow)
	if err != nil || head == nil {
		if step.WriteEffect {
			return false, fmt.Errorf("step %s: the first step of its call (%s) has no record; a write step does not run without it", step.ID, headRow)
		}
		return false, nil
	}
	switch {
	case head.Status == "skipped":
		e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("skipped"), FinishedAt: now(),
			SkipReason: str("the first step of its call (" + headRow + ") was skipped")})
		return true, nil
	case !step.WriteEffect:
		return false, nil
	case head.Status != "done":
		return false, fmt.Errorf("step %s: the first step of its call (%s) is %s, not done; a write step runs only after it", step.ID, headRow, head.Status)
	case head.Resolution != "approved":
		return false, fmt.Errorf("step %s: the first step of its call (%s) was not approved; a write step runs only after an approval", step.ID, headRow)
	}
	return false, nil
}

// dirPrint is a fingerprint of every file in a call directory: names and
// contents, in name order.
func dirPrint(dir string) string {
	h := sha256.New()
	var names []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	for _, p := range names {
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%d:%s%d:", len(rel), rel, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func callFilesKey(runID uuid.UUID, dir string) string { return runID.String() + "|" + dir }

// checkCallFiles refuses a write step of a call whose files changed since the
// call's previous step finished — after the approval, so what runs is what
// was approved. The first step of a call has nothing to compare.
func (e *Engine) checkCallFiles(runID uuid.UUID, step *workflow.Step, dir string) error {
	if !step.WriteEffect || step.WriteEntry || step.CallHead == "" || step.CallHead == step.CallSelf {
		return nil
	}
	want, ok := e.callFiles.Load(callFilesKey(runID, dir))
	if !ok {
		return fmt.Errorf("step %s: the record of what its call's earlier steps wrote is gone (the server restarted?); run the call again — nothing was written", step.ID)
	}
	if got := dirPrint(dir); got != want.(string) {
		return fmt.Errorf("step %s: the call's files changed after it was approved (something wrote to them between its steps); nothing was written", step.ID)
	}
	return nil
}

// recordCallFiles keeps the fingerprint of a call's files after one of its
// write steps ran, for the next step to compare.
func (e *Engine) recordCallFiles(runID uuid.UUID, step *workflow.Step, dir string) {
	if step.WriteEffect {
		e.callFiles.Store(callFilesKey(runID, dir), dirPrint(dir))
	}
}

// lastOfCall reports whether no other step of step's call needs it — the
// point after which the call's directory is no longer read.
func lastOfCall(def *workflow.Definition, step *workflow.Step) bool {
	if def == nil || step.CallHead == "" {
		return true
	}
	for _, s := range def.Steps {
		if s.CallHead != step.CallHead || s.CallSelf == step.CallSelf {
			continue
		}
		for _, n := range s.Needs {
			if n == step.CallSelf {
				return false
			}
		}
	}
	return true
}

// defOfRun is the loaded definition a run executes, or nil.
func (e *Engine) defOfRun(ctx context.Context, runID uuid.UUID) *workflow.Definition {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		return nil
	}
	return e.Definitions()[run.Workflow]
}
