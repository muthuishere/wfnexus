package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/shell"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A workflow has three kinds of node, and only one of them calls an agent:
//
//	prompt:  an agent — a loop, tools, a budget, a schema-validated submission
//	run:     a command — deterministic, no model, no cost
//	judge:   a decision — typed questions on a small model, no tools
//
// All three produce schema-validated facts, so a guard or a gate reads
// `tests.ok`, `route.desk` and `triage.valid` the same way and does not care
// which kind produced them. That uniformity is the point: the cheap nodes are
// not a lesser thing bolted on, they are the same contract at a lower price.

// shellFor resolves the interpreter for a `run` step: what it names, else the
// best one this machine has.
func shellFor(step *workflow.Step) (shell.Shell, error) {
	if step.Shell != "" {
		return shell.Lookup(step.Shell)
	}
	return shell.Default()
}

// runCommand executes a `run` node in the run's workspace.
func (e *Engine) runCommand(ctx context.Context, runID uuid.UUID, step *workflow.Step, data workflow.TemplateData) (map[string]any, error) {
	cmd, err := workflow.Render(step.Run, data)
	if err != nil {
		return nil, fmt.Errorf("render run: %w", err)
	}
	// Where it runs is the step's own `runs-on`. A label this process serves
	// runs here; anything else is queued for a worker holding that label and
	// comes back with the same shape, so nothing downstream can tell.
	if !e.servesLocally(step.RunsOn) {
		return e.runRemote(ctx, runID, step, cmd, step.RunsOn, data)
	}
	e.setStep(ctx, runID, step.ID, store.StepPatch{
		Status: str("running"), Prompt: str(cmd), StartedAt: now(), Error: str(""), ClearPending: true,
	})
	e.emit(ctx, runID, step.ID, "tool_call", map[string]any{"name": "run", "args": map[string]any{"command": cmd}})

	if step.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSec)*time.Second)
		defer cancel()
	}
	// The interpreter is resolved, never assumed. `bash -lc` was hardcoded
	// here, which meant a `run:` step could not execute on a machine without
	// bash — and `-l` sourced the operator's profile, so the command saw a PATH
	// and an environment that varied per developer. A workflow written down
	// must not depend on somebody's dotfiles.
	sh, err := shellFor(step)
	if err != nil {
		return nil, err
	}
	argv := sh.Command(cmd)
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if data.WorkDir != "" {
		c.Dir = data.WorkDir
	}
	// The step's env, resolved against THIS machine. A reference to a variable
	// nobody set is an error naming the variable, rather than an empty string
	// and a 401 somewhere far away.
	stepEnv, err := workflow.ResolveEnv(step.Env)
	if err != nil {
		return nil, fmt.Errorf("step %s: %w", step.ID, err)
	}
	c.Env = append(os.Environ(), stepEnv...)
	var stdout, stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()

	exitCode := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			// could not start at all — a platform fault, not the command's verdict
			return nil, fmt.Errorf("could not run %q: %w", cmd, runErr)
		}
	}
	out := map[string]any{
		"ok": exitCode == 0, "exitCode": exitCode,
		"stdout": trim(stdout.String(), maxEventOutput), "stderr": trim(stderr.String(), maxEventOutput),
	}
	e.emit(ctx, runID, step.ID, "tool_result", map[string]any{
		"name": "run", "isError": exitCode != 0,
		"output": trim(stdout.String()+stderr.String(), maxEventOutput),
	})
	// A non-zero exit is the command's ANSWER, not a platform failure: the step
	// succeeds and the workflow decides what a red suite means via a gate.
	return out, nil
}

// runJudge executes a `judge` node: typed questions, no agent, no tools.
func (e *Engine) runJudge(ctx context.Context, runID uuid.UUID, step *workflow.Step, data workflow.TemplateData) (map[string]any, error) {
	e.setStep(ctx, runID, step.ID, store.StepPatch{
		Status: str("running"), StartedAt: now(), Error: str(""), ClearPending: true,
	})
	judge := *step.Judge
	// decide() reads step.Decide, so present the judge block through it
	probe := *step
	probe.Decide = &judge
	rec, vals, err := e.decide(ctx, runID, &probe, data)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("step %s: the judge produced no decision", step.ID)
	}
	e.setStep(ctx, runID, step.ID, store.StepPatch{Decision: mustJSON(rec)})
	return vals, nil
}

// isAgent reports whether a step actually runs an agent — the expensive kind.
func isAgent(s *workflow.Step) bool { return s.Run == "" && s.Judge == nil }
