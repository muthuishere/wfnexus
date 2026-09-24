package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// STATE — what a workflow remembers between runs.
//
// Four scopes, narrowest to widest, each read in a template under its own
// name:
//
//	{{ .Step.x }}      this step of this workflow, across runs
//	{{ .Workflow.x }}  this workflow, across runs
//	{{ .Project.x }}   every workflow in this repository
//	{{ .Global.x }}    everything on this platform
//
// FOUR NAMESPACES, NOT A CASCADE. `.Step.last_id` never falls back to
// `.Workflow.last_id`. Environment cascades because it is ONE environment
// assembled from layers — system under project under file — and the thing being
// produced is a single map. State is four separate places to put things, and a
// silent fallback there would turn "nobody has written this yet" into "here is
// somebody else's value", which is the worst possible answer for the incremental
// case state exists to serve: a workflow resuming from a watermark it never set.
//
// It is NOT a secret store: values are plaintext in the database. env.go is the
// sealed one.

// stateNameFor is where a scope's rows live. The step scope is keyed by the
// workflow AND the step, because two workflows may both have a step called
// `scan` and they are not the same memory.
func stateNameFor(scope, workflowName, project, stepID string) (string, error) {
	switch scope {
	case model.StateScopeGlobal:
		return "", nil
	case model.StateScopeProject:
		if project == "" {
			return "", fmt.Errorf("project state needs a project — this run has none")
		}
		return project, nil
	case model.StateScopeWorkflow:
		if workflowName == "" {
			return "", fmt.Errorf("workflow state needs a workflow name")
		}
		return workflowName, nil
	case model.StateScopeStep:
		if workflowName == "" || stepID == "" {
			return "", fmt.Errorf("step state needs a workflow and a step id")
		}
		return workflowName + "/" + stepID, nil
	}
	return "", fmt.Errorf("unknown state scope %q — one of %s", scope, strings.Join(model.StateScopes, ", "))
}

// stateData loads the four namespaces a step's templates may read.
//
// It is read FRESH for each step rather than once per run, because a step that
// writes state with `wfx state set` must be visible to the next step in the same
// run — otherwise "increment a counter" would need two runs to be believed.
//
// A worker engine has no store (ports.go); there the four maps are empty and a
// reference renders empty, exactly as an unwritten key does.
func (e *Engine) stateData(ctx context.Context, workflowName, project, stepID string) (step, wf, proj, global map[string]string) {
	step, wf, proj, global = map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
	if e.store == nil {
		return
	}
	load := func(scope string, dst *map[string]string) {
		name, err := stateNameFor(scope, workflowName, project, stepID)
		if err != nil {
			return // that scope has no address for this step; stays empty
		}
		if m, err := e.store.StateFor(ctx, scope, name); err == nil {
			*dst = m
		}
	}
	load(model.StateScopeStep, &step)
	load(model.StateScopeWorkflow, &wf)
	load(model.StateScopeProject, &proj)
	load(model.StateScopeGlobal, &global)
	return
}

// withState fills a TemplateData's four state namespaces for one step. Every
// place that builds TemplateData goes through here, so no path can quietly
// render a prompt with the state maps missing.
func (e *Engine) withState(ctx context.Context, data workflow.TemplateData, def *workflow.Definition, runID uuid.UUID, stepID string) workflow.TemplateData {
	name := ""
	if def != nil {
		name = def.Name
	}
	data.Step, data.Workflow, data.Project, data.Global = e.stateData(ctx, name, e.projectOfRun(ctx, runID), stepID)
	return data
}

// persistStepState applies a step's `state:` block once its output is accepted.
//
// This is how an AGENT writes state. The step declares which of its output
// fields persist, and the ENGINE writes them — so the write is deterministic,
// visible in the YAML, and needs neither a new built-in tool nor the model's
// cooperation. It is also the only write path a `runs-on:` step has, because the
// value is rendered and stored here, on the server, where the database is.
func (e *Engine) persistStepState(ctx context.Context, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData, out map[string]any) {
	if e.store == nil || step == nil || len(step.State) == 0 {
		return
	}
	data.Output = out
	name := ""
	if def != nil {
		name = def.Name
	}
	project := ""
	if id, err := uuid.Parse(data.RunID); err == nil {
		project = e.projectOfRun(ctx, id)
	}
	for _, scope := range model.StateScopes {
		entries := step.State[scope]
		if len(entries) == 0 {
			continue
		}
		scopeName, err := stateNameFor(scope, name, project, step.ID)
		if err != nil {
			e.emit(ctx, uuidOrNil(data.RunID), step.ID, "error", map[string]any{"message": "state: " + err.Error()})
			continue
		}
		for key, tmpl := range entries {
			val, err := workflow.Render(tmpl, data)
			if err != nil {
				e.emit(ctx, uuidOrNil(data.RunID), step.ID, "error", map[string]any{"message": fmt.Sprintf("state %s.%s: %v", scope, key, err)})
				continue
			}
			if err := e.store.PutState(ctx, scope, scopeName, key, val); err != nil {
				e.emit(ctx, uuidOrNil(data.RunID), step.ID, "error", map[string]any{"message": fmt.Sprintf("state %s.%s: %v", scope, key, err)})
				continue
			}
			e.emit(ctx, uuidOrNil(data.RunID), step.ID, "log", map[string]any{
				"message": fmt.Sprintf("state %s.%s = %s", scope, key, trim(val, 200)),
			})
		}
	}
}

func uuidOrNil(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// ---- managing the store, for the API and the CLI ----

// ErrNoStateStore is what a worker engine answers: it has no database, by
// design, and inventing one for state would be the second place a run's truth
// lives.
var ErrNoStateStore = fmt.Errorf("this process has no database — state lives on the server")

// StateAddress resolves a scope plus the run's own facts into (scope, name).
// The caller never passes a project or a workflow name it chose: a step that
// could name the scope's OWNER could write another repository's state.
func StateAddress(scope, workflowName, project, stepID string) (string, string, error) {
	name, err := stateNameFor(scope, workflowName, project, stepID)
	return scope, name, err
}

func (e *Engine) SetState(ctx context.Context, scope, scopeName, key, value string) error {
	if e.store == nil {
		return ErrNoStateStore
	}
	if key == "" {
		return fmt.Errorf("a state entry needs a key")
	}
	if _, err := stateNameFor(scope, "x", "x", "x"); err != nil {
		return err
	}
	if scope != model.StateScopeGlobal && scopeName == "" {
		return fmt.Errorf("%s state needs a name", scope)
	}
	return e.store.PutState(ctx, scope, scopeName, key, value)
}

func (e *Engine) GetState(ctx context.Context, scope, scopeName, key string) (string, bool, error) {
	if e.store == nil {
		return "", false, ErrNoStateStore
	}
	return e.store.GetState(ctx, scope, scopeName, key)
}

func (e *Engine) DeleteState(ctx context.Context, scope, scopeName, key string) error {
	if e.store == nil {
		return ErrNoStateStore
	}
	return e.store.DeleteState(ctx, scope, scopeName, key)
}

func (e *Engine) ListState(ctx context.Context, scope, scopeName string) ([]model.StateVar, error) {
	if e.store == nil {
		return nil, ErrNoStateStore
	}
	return e.store.ListState(ctx, scope, scopeName)
}

// WorkflowState is everything one workflow's page should show: its own scope,
// each of its steps' scopes, and the global namespace every workflow shares.
// Project state is not in here because a workflow belongs to a project only
// through a run.
func (e *Engine) WorkflowState(ctx context.Context, name string) ([]model.StateVar, error) {
	if e.store == nil {
		return nil, ErrNoStateStore
	}
	out := []model.StateVar{}
	wf, err := e.store.ListState(ctx, model.StateScopeWorkflow, name)
	if err != nil {
		return nil, err
	}
	out = append(out, wf...)
	steps, err := e.store.ListStateByPrefix(ctx, model.StateScopeStep, name+"/")
	if err != nil {
		return nil, err
	}
	out = append(out, steps...)
	global, err := e.store.ListState(ctx, model.StateScopeGlobal, "")
	if err != nil {
		return nil, err
	}
	return append(out, global...), nil
}

// RunStateAddress resolves a scope against a RUN — the only way a step is
// allowed to address state. The step names a scope and, for the step scope, its
// own id; the workflow and the project come from the run row, so a step cannot
// reach another repository's state by naming it.
func (e *Engine) RunStateAddress(ctx context.Context, runID uuid.UUID, scope, stepID string) (string, error) {
	if e.store == nil {
		return "", ErrNoStateStore
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	return stateNameFor(scope, run.Workflow, run.Project, stepID)
}
