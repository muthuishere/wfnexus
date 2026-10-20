package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/planner"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// buildPlan turns a definition into the planner's view. The run's input keys
// are the facts that hold before anything runs.
func buildPlan(def *workflow.Definition, input map[string]any) planner.Plan {
	p := planner.Plan{Goal: def.Goal}
	for k := range input {
		p.Given = append(p.Given, k)
	}
	for i := range def.Steps {
		s := &def.Steps[i]
		p.Actions = append(p.Actions, planner.Action{
			ID:       s.ID,
			Consumes: s.Consumes,
			Produces: s.Produces,
			Guard:    guardFor(s.When),
		})
	}
	return p
}

// guardFor compiles a step's `when` conditions. All must hold.
func guardFor(gs []workflow.Guard) func(*planner.World) bool {
	if len(gs) == 0 {
		return nil
	}
	conds := append([]workflow.Guard(nil), gs...)
	return func(w *planner.World) bool {
		for _, g := range conds {
			v, ok := w.Value(g.Path)
			if g.Exists != nil {
				if ok != *g.Exists {
					return false
				}
				continue
			}
			if !ok || string(mustJSON(v)) != string(mustJSON(g.Equals)) {
				return false
			}
		}
		return true
	}
}

// guardWhy is guardFor's answer in words: the first condition that does not
// hold, what it wanted and what the world had — "when classify.change_type =
// "code" (was "config")". It reads the same World with the same rules
// (present-but-null EXISTS; values compare as JSON), so the reason shown is
// the reason the engine acted on. Empty when every condition holds.
func guardWhy(gs []workflow.Guard, w *planner.World) string {
	for _, g := range gs {
		v, ok := w.Value(g.Path)
		if g.Exists != nil {
			if ok != *g.Exists {
				if *g.Exists {
					return fmt.Sprintf("when %s exists (it does not)", g.Path)
				}
				return fmt.Sprintf("when %s is absent (it is %s)", g.Path, mustJSON(v))
			}
			continue
		}
		if !ok {
			return fmt.Sprintf("when %s = %s (%s)", g.Path, mustJSON(g.Equals), missing(g.Path, w))
		}
		if string(mustJSON(v)) != string(mustJSON(g.Equals)) {
			return fmt.Sprintf("when %s = %s (was %s)", g.Path, mustJSON(g.Equals), mustJSON(v))
		}
	}
	return ""
}

// missing says why a guard path did not resolve: the step behind it never
// produced anything, or it did and the field is not there. The run input
// always exists, so an unresolved `input.*` is a key nobody passed.
func missing(path string, w *planner.World) string {
	head, _, _ := strings.Cut(path, ".")
	if _, ok := w.Value(head); !ok && head != "input" {
		return head + " did not run"
	}
	return "not set"
}

// runPlanned executes a workflow whose ORDER IS DERIVED. Every step whose facts
// are satisfied runs concurrently; after each completion the world changes and
// the plan is computed again, so the route responds to what the agents actually
// found rather than to what the author guessed.
func (e *Engine) runPlanned(ctx context.Context, runID uuid.UUID, def *workflow.Definition, input map[string]any, workdir, baseRef string) error {
	plan := buildPlan(def, input)
	if err := plan.Validate(); err != nil {
		return err
	}

	var mu sync.Mutex
	world := planner.NewWorld()
	for k, v := range input {
		world.Assert(k, v)
	}
	outputs := map[string]any{}
	done := map[string]bool{}

	// Replay: facts established by a previous attempt still hold.
	existing, err := e.store.ListSteps(ctx, runID)
	if err != nil {
		return err
	}
	byID := map[string]*workflow.Step{}
	for i := range def.Steps {
		byID[def.Steps[i].ID] = &def.Steps[i]
	}
	for _, st := range existing {
		if st.Status != "done" && st.Status != "skipped" {
			continue
		}
		done[st.StepID] = true
		var out any
		if len(st.Output) > 0 {
			_ = json.Unmarshal(st.Output, &out)
			outputs[st.StepID] = out
		}
		assertProduces(world, byID[st.StepID], st.StepID, out)
	}

	limit := def.MaxParallel
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var halted bool

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		mu.Lock()
		if plan.Done(world, done) {
			mu.Unlock()
			break
		}
		if halted {
			mu.Unlock()
			return nil // the step already recorded the terminal status
		}
		ready := plan.Ready(world, done)
		if len(ready) == 0 {
			err := plan.Stuck(world, done)
			mu.Unlock()
			// A cancelled run is not a stuck plan. Report what happened.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		snapshot := make(map[string]any, len(outputs))
		for k, v := range outputs {
			snapshot[k] = v
		}
		for _, a := range ready {
			// Pre-claim so a step cannot be started twice if the wave is
			// re-entered. The outcome decides whether the claim stands: a halt
			// or a cancellation releases it below, because a step that never ran
			// must not read as done.
			done[a.ID] = true
		}
		mu.Unlock()

		e.emit(ctx, runID, "", "plan", map[string]any{
			"ready": ids(ready), "facts": world.Facts(), "goal": def.Goal,
		})

		var wave sync.WaitGroup
		for _, a := range ready {
			step := byID[a.ID]
			wave.Add(1)
			go func() {
				defer wave.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					// Release the claim. Leaving it set marked a step done that
					// had not run, and the next iteration then reported "goal
					// cannot be reached" for a run that was simply cancelled —
					// found by code-review on this very change.
					mu.Lock()
					delete(done, step.ID)
					mu.Unlock()
					return
				}
				data := workflow.TemplateData{
					RunID: runID.String(), WorkDir: workdir, BaseRef: baseRef,
					Input: input, Steps: snapshot,
				}
				out, outcome := e.runOneStep(ctx, runID, def, step, data)
				mu.Lock()
				defer mu.Unlock()
				switch outcome {
				case stepDone:
					outputs[step.ID] = out
					assertProduces(world, step, step.ID, out)
				case stepSkipped:
					// stays done, produces nothing
				default:
					delete(done, step.ID)
					halted = true
				}
			}()
		}
		wave.Wait()
	}

	e.skipUntaken(ctx, runID, "not taken by the planner")
	e.setRun(ctx, runID, "done", "", "")
	return nil
}

// assertProduces records a completed step's facts. The step id is always a
// fact, so a workflow can depend on "this step ran" without naming an output.
func assertProduces(w *planner.World, step *workflow.Step, id string, out any) {
	w.Assert(id, out)
	if step == nil {
		return
	}
	for _, f := range step.Produces {
		w.Assert(f, out)
	}
}

func ids(as []planner.Action) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}

var _ = model.StepPatch{}
var _ = fmt.Sprintf
