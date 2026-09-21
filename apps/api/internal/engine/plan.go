package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/planner"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
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
			return err
		}
		snapshot := make(map[string]any, len(outputs))
		for k, v := range outputs {
			snapshot[k] = v
		}
		for _, a := range ready {
			done[a.ID] = true // claimed; the result decides whether it stays done
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

var _ = store.StepPatch{}
var _ = fmt.Sprintf
