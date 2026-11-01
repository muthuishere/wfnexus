package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/planner"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// runLoop executes a step that repeats (workflow/loop.go). It is reached from
// runOneStep, so the sequential, DAG and planned paths all get loops from one
// place and cannot drift.
//
// Every iteration is an ordinary step row — `review[2]`, `fix-loop[1].check`
// — executed by runOneStep itself. That is the whole trick: an iteration is
// persisted, retried, gated, approved and resumed by the code that already
// does those things for a step, rather than by a second copy of it. A resume
// replays the iterations whose rows are done and continues with the first
// that is not.
//
// The loop step's own row carries the aggregate {iterations, count, last}.
func (e *Engine) runLoop(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData) (map[string]any, stepOutcome) {
	st, err := e.store.GetStep(ctx, runID, step.ID)
	if err != nil {
		e.failStep(ctx, runID, step.ID, err)
		return nil, stepHalted
	}
	// A loop over a body is approved once, before it starts; a single-step
	// loop asks per iteration (each child carries requires_approval).
	if step.ApprovesOnce() && st.Status != "approved" {
		e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("awaiting_approval")})
		e.setRun(ctx, runID, "awaiting_approval", step.ID, "")
		e.notifyPause(ctx, runID, step.ID, "approval", "step "+step.ID+" needs approval before it runs", nil)
		return nil, stepHalted
	}
	e.setRun(ctx, runID, "running", step.ID, "")
	patch := model.StepPatch{Status: str("running"), Error: str("")}
	if st.StartedAt == nil {
		patch.StartedAt = now()
	}
	e.setStep(ctx, runID, step.ID, patch)

	var results []any
	var outcome stepOutcome
	var failure string
	switch step.LoopMode() {
	case "foreach":
		results, outcome, failure = e.runForeach(ctx, runID, def, step, st.Position, data)
	default:
		results, outcome, failure = e.runRepeat(ctx, runID, def, step, st.Position, data)
	}
	if failure != "" {
		e.failStep(ctx, runID, step.ID, fmt.Errorf("%s", failure))
		return nil, stepHalted
	}
	if outcome == stepHalted {
		// The iteration set the run's status. A failed iteration fails the
		// loop's row too, so the run never shows a failed run with its loop
		// still "running"; a pause leaves it running — it will resume.
		if run, err := e.store.GetRun(ctx, runID); err == nil && (run.Status == "failed" || run.Status == "cancelled") {
			e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("failed"), Error: str("iteration " + run.CurrentStep + " " + run.Status + ": " + run.Error), FinishedAt: now()})
			e.setRun(ctx, runID, run.Status, run.CurrentStep, run.Error)
		}
		return nil, stepHalted
	}
	agg := map[string]any{"iterations": results, "count": len(results)}
	if len(results) > 0 {
		agg["last"] = results[len(results)-1]
	}
	e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("done"), Output: mustJSON(agg), FinishedAt: now()})
	return agg, stepDone
}

// runForeach runs one iteration per item, at most `parallel` at once.
func (e *Engine) runForeach(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, pos int, data workflow.TemplateData) ([]any, stepOutcome, string) {
	items, err := foreachItems(step, data)
	if err != nil {
		return nil, stepHalted, err.Error()
	}
	if len(items) > workflow.MaxLoopIterations || (step.MaxIterations > 0 && len(items) > step.MaxIterations) {
		limit := step.MaxIterations
		if limit == 0 {
			limit = workflow.MaxLoopIterations
		}
		return nil, stepHalted, fmt.Sprintf("foreach %s has %d items, over max_iterations %d — refusing rather than running a surprise", step.ForeachPath(), len(items), limit)
	}
	// Every iteration's rows exist from the start, so the run shows the whole
	// plan — three pending reviews — before the first one finishes.
	for i := range items {
		if err := e.ensureIterationRows(ctx, runID, step, pos, i); err != nil {
			return nil, stepHalted, err.Error()
		}
	}
	e.emit(ctx, runID, step.ID, "loop.planned", map[string]any{"mode": "foreach", "iterations": len(items), "parallel": parallelOf(step)})

	results := make([]any, len(items))
	sem := make(chan struct{}, parallelOf(step))
	var wg sync.WaitGroup
	var mu sync.Mutex
	halted := false
	for i, item := range items {
		mu.Lock()
		stop := halted
		mu.Unlock()
		if stop || ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		// Re-check after waiting: an iteration that halted while this one
		// waited for a slot means the run is parked, and starting more work
		// on a parked run is the thing a halt exists to prevent.
		mu.Lock()
		stop = halted
		mu.Unlock()
		if stop {
			<-sem
			break
		}
		wg.Add(1)
		go func(i int, item any) {
			defer wg.Done()
			defer func() { <-sem }()
			out, oc := e.runIteration(ctx, runID, def, step, pos, i, item, data, nil)
			mu.Lock()
			defer mu.Unlock()
			if oc == stepHalted {
				halted = true
				return
			}
			results[i] = out
		}(i, item)
	}
	wg.Wait()
	if halted || ctx.Err() != nil {
		return nil, stepHalted, ""
	}
	return results, stepDone, ""
}

// runRepeat runs an until/while loop: iterations one after another, the
// condition read after each against what that iteration produced. The next
// iteration sees the previous one's outputs as `.Steps.<id>` — the fix step
// reads why the check failed last time.
func (e *Engine) runRepeat(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, pos int, data workflow.TemplateData) ([]any, stepOutcome, string) {
	cond := guardFor(step.LoopGuards())
	var results []any
	var carry map[string]any
	var lastWorld *planner.World
	for i := 0; i < step.MaxIterations; i++ {
		if ctx.Err() != nil {
			return nil, stepHalted, ""
		}
		if err := e.ensureIterationRows(ctx, runID, step, pos, i); err != nil {
			return nil, stepHalted, err.Error()
		}
		out, oc := e.runIteration(ctx, runID, def, step, pos, i, nil, data, carry)
		if oc == stepHalted {
			return nil, stepHalted, ""
		}
		results = append(results, out)
		carry = iterationFacts(step, out)
		lastWorld = loopWorld(data, carry)
		holds := cond(lastWorld)
		e.emit(ctx, runID, step.ID, "loop.iteration", map[string]any{"mode": step.LoopMode(), "index": i, "condition": holds})
		if (step.LoopMode() == "until" && holds) || (step.LoopMode() == "while" && !holds) {
			return results, stepDone, ""
		}
	}
	why := ""
	if lastWorld != nil {
		if w := guardWhy(step.LoopGuards(), lastWorld); w != "" {
			why = " — last check: " + w
		}
	}
	if step.LoopMode() == "until" {
		return nil, stepHalted, fmt.Sprintf("until condition not met after %d iterations (max_iterations)%s", step.MaxIterations, why)
	}
	return nil, stepHalted, fmt.Sprintf("while condition still holds after %d iterations (max_iterations)", step.MaxIterations)
}

// runIteration runs ONE iteration: the step itself, or its body in order.
// carry is the previous iteration's outputs, visible as `.Steps.<id>`.
func (e *Engine) runIteration(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, pos, i int, item any, data workflow.TemplateData, carry map[string]any) (any, stepOutcome) {
	idata := data
	idata.Item, idata.Index = item, i
	idata.Steps = make(map[string]any, len(data.Steps)+len(carry))
	for k, v := range data.Steps {
		idata.Steps[k] = v
	}
	for k, v := range carry {
		idata.Steps[k] = v
	}

	if !step.IsGroup() {
		child := iterationStep(step, workflow.IterationID(step.ID, i, ""))
		if out, ok := e.replayed(ctx, runID, child.ID); ok {
			return out, stepDone
		}
		out, oc := e.runOneStep(ctx, runID, def, &child, idata)
		if oc != stepDone {
			return nil, stepHalted
		}
		return out, stepDone
	}

	outs := map[string]any{}
	for k := range step.Steps {
		child := iterationStep(&step.Steps[k], workflow.IterationID(step.ID, i, step.Steps[k].ID))
		body := step.Steps[k].ID
		if out, ok := e.replayed(ctx, runID, child.ID); ok {
			if out != nil {
				outs[body], idata.Steps[body] = out, out
			}
			continue
		}
		// A body step's `when` reads this iteration's facts, so a step can
		// be skipped for one item and run for the next.
		if e.skipUnlessGuarded(ctx, runID, &child, idata) {
			delete(idata.Steps, body)
			continue
		}
		out, oc := e.runOneStep(ctx, runID, def, &child, idata)
		switch oc {
		case stepDone:
			outs[body], idata.Steps[body] = out, out
		case stepSkipped:
		default:
			return nil, stepHalted
		}
	}
	return outs, stepDone
}

// iterationStep is the step an iteration row executes: the body (or the loop
// step itself) under the iteration's id, with nothing of the loop left on it.
func iterationStep(src *workflow.Step, id string) workflow.Step {
	c := *src
	c.ID = id
	c.Foreach, c.Until, c.While, c.Steps = nil, nil, nil, nil
	c.MaxIterations, c.Parallel = 0, 0
	if !src.IsLoop() {
		return c
	}
	// The loop step's own `when` was already applied before the loop began.
	c.When = nil
	return c
}

// replayed returns an iteration row's output if a previous attempt of this
// run already finished it — the resume path. A skipped row replays as nil.
func (e *Engine) replayed(ctx context.Context, runID uuid.UUID, id string) (any, bool) {
	st, err := e.store.GetStep(ctx, runID, id)
	if err != nil || (st.Status != "done" && st.Status != "skipped") {
		return nil, false
	}
	if st.Status == "skipped" || len(st.Output) == 0 {
		return nil, true
	}
	var out map[string]any
	_ = json.Unmarshal(st.Output, &out)
	return out, true
}

func (e *Engine) ensureIterationRows(ctx context.Context, runID uuid.UUID, step *workflow.Step, pos, i int) error {
	if !step.IsGroup() {
		return e.store.EnsureStep(ctx, runID, workflow.IterationID(step.ID, i, ""), workflow.IterationPosition(pos, i, 0))
	}
	for k, b := range step.Steps {
		if err := e.store.EnsureStep(ctx, runID, workflow.IterationID(step.ID, i, b.ID), workflow.IterationPosition(pos, i, k+1)); err != nil {
			return err
		}
	}
	return nil
}

// iterationFacts is what the loop condition — and the next iteration — reads:
// the step's own output under its id, or each body step's under its own.
func iterationFacts(step *workflow.Step, out any) map[string]any {
	if !step.IsGroup() {
		return map[string]any{step.ID: out}
	}
	m, _ := out.(map[string]any)
	facts := make(map[string]any, len(m))
	for k, v := range m {
		facts[k] = v
	}
	return facts
}

func loopWorld(data workflow.TemplateData, facts map[string]any) *planner.World {
	w := planner.NewWorld()
	for id, out := range data.Steps {
		w.Assert(id, out)
	}
	for id, out := range facts {
		w.Assert(id, out)
	}
	if _, clash := facts["input"]; !clash {
		if _, clash := data.Steps["input"]; !clash {
			w.Assert("input", data.Input)
		}
	}
	return w
}

func parallelOf(step *workflow.Step) int {
	if step.Parallel > 1 {
		return step.Parallel
	}
	return 1
}

// foreachItems resolves what a foreach iterates: a literal list, or the array
// a path names in the run input or an earlier step's output.
func foreachItems(step *workflow.Step, data workflow.TemplateData) ([]any, error) {
	if l := step.ForeachLiteral(); l != nil {
		return l, nil
	}
	path := step.ForeachPath()
	v, ok := workflow.ResolvePath(path, data.Input, data.Steps)
	if !ok || v == nil {
		return nil, fmt.Errorf("foreach %s: nothing there — the step that produces it did not run, or the input was not given", path)
	}
	switch l := v.(type) {
	case []any:
		return l, nil
	case []string:
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out, nil
	}
	// A JSON round trip normalises any other slice type ([]map..., []int).
	var out []any
	if err := json.Unmarshal(mustJSON(v), &out); err != nil {
		return nil, fmt.Errorf("foreach %s: is %T, not a list", path, v)
	}
	return out, nil
}

// isIterationRow reports a run row that belongs to a loop iteration rather
// than to a step of the definition. Replays that count definition steps skip
// these: the loop step's own row is what says whether the loop finished.
func isIterationRow(id string) bool { return workflow.IsIterationID(id) }

// resetFrom is Retry's reset: stepID and everything after it go back to
// pending, iteration rows included — but not the iteration rows of a loop
// that finished BEFORE stepID, which still are the record of what it did.
//
// Iteration rows sort after every definition step (IterationPosition), so
// "everything after" is two ranges: the definition steps from stepID on, and
// the iteration rows of those steps. Retrying an iteration row itself resets
// it and every later iteration, and the definition steps after its loop.
func (e *Engine) resetFrom(ctx context.Context, runID uuid.UUID, def *workflow.Definition, stepID string) error {
	if isIterationRow(stepID) {
		parent, _, _, _ := workflow.SplitIterationID(stepID)
		ppos, _ := def.Step(parent)
		if ppos < 0 {
			return fmt.Errorf("unknown step %q", stepID)
		}
		st, err := e.store.GetStep(ctx, runID, stepID)
		if err != nil {
			return fmt.Errorf("unknown step %q: %w", stepID, err)
		}
		if err := e.store.ResetStepsRange(ctx, runID, ppos+1, workflow.IterationBase(0)); err != nil {
			return err
		}
		return e.store.ResetStepsRange(ctx, runID, st.Position, -1)
	}
	pos, _ := def.Step(stepID)
	if pos < 0 {
		return fmt.Errorf("unknown step %q", stepID)
	}
	if err := e.store.ResetStepsRange(ctx, runID, pos, workflow.IterationBase(0)); err != nil {
		return err
	}
	return e.store.ResetStepsRange(ctx, runID, workflow.IterationBase(pos), -1)
}
