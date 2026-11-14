package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/shell"
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
	// Values reach the script through the environment (workflow/runtext.go);
	// cmd is the script as the run page shows it, with references only.
	if err := workflow.CheckRunText(step.Run, step.Shell); err != nil {
		return nil, fmt.Errorf("step %s: %w", step.ID, err)
	}
	data.Const = step.RunConst
	rt, err := workflow.RenderRun(step.Run, data)
	if err != nil {
		return nil, fmt.Errorf("render run: %w", err)
	}
	cmd := rt.Display()
	// Where it runs is the step's own `runs-on`. A label this process serves
	// runs here; anything else is queued for a worker holding that label and
	// comes back with the same shape, so nothing downstream can tell.
	// A label may be templated (a module command's `runs-on: "{{ .Inputs.machine }}"`
	// reached through `wfx module call` renders from the run input here).
	label := step.RunsOn
	if strings.Contains(label, "{{") {
		if label, err = workflow.Render(label, data); err != nil {
			return nil, fmt.Errorf("render runs-on: %w", err)
		}
		label = strings.TrimSpace(label)
	}
	if !e.servesLocally(label) {
		return e.runRemote(ctx, runID, step, rt, label, data)
	}
	e.setStep(ctx, runID, step.ID, model.StepPatch{
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
	dataDir := ""
	if len(rt.Data) > 0 {
		if dataDir, err = os.MkdirTemp("", "wfx-data-"); err != nil {
			return nil, fmt.Errorf("step %s: %w", step.ID, err)
		}
		defer os.RemoveAll(dataDir)
	}
	script, dataEnv, err := rt.For(sh.Name, dataDir)
	if err != nil {
		return nil, fmt.Errorf("step %s: %w", step.ID, err)
	}
	argv := sh.Command(script)
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if data.WorkDir != "" {
		c.Dir = data.WorkDir
	}
	// A module command's step runs in its own call directory, so two calls
	// running at once never share their in.* files. It is under the run's
	// own directory, not the checkout, so nothing there is ever committed.
	callDir := ""
	if step.Module != "" && e.cfg.WorkDir != "" {
		callDir = filepath.Join(e.cfg.WorkDir, runID.String(), "calls", CallKey(step.ID))
		if err := os.MkdirAll(callDir, 0o700); err != nil {
			return nil, fmt.Errorf("step %s: call directory: %w", step.ID, err)
		}
		c.Dir = callDir
		if e.testHookBeforeCallStep != nil {
			e.testHookBeforeCallStep(step, callDir)
		}
		// The files a call's earlier steps wrote (in.*, plan.json) are what
		// this write step acts on; they must be exactly what those steps
		// left, after the approval (callgate.go).
		if err := e.checkCallFiles(runID, step, callDir); err != nil {
			return nil, err
		}
		defer e.recordCallFiles(runID, step, callDir)
	}
	// The whole cascade — system, project, then the file — resolved against
	// THIS machine. A reference to a variable nobody set is an error naming the
	// variable, rather than an empty string and a 401 somewhere far away.
	env, err := e.stepEnv(ctx, runID, step, data.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("step %s: %w", step.ID, err)
	}
	stepEnv, err := workflow.ResolveEnv(env)
	if err != nil {
		return nil, fmt.Errorf("step %s: %w", step.ID, err)
	}
	// The step's story files (story.go): summary, outcome, state.
	story, err := newStoryFiles()
	if err != nil {
		return nil, fmt.Errorf("step %s: story files: %w", step.ID, err)
	}
	defer story.close()
	for k, v := range story.env() {
		stepEnv = append(stepEnv, k+"="+v)
	}
	c.Env = append(append(os.Environ(), stepEnv...), dataEnv...)
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
	if oc := e.collectStory(ctx, runID, step.ID, story, out); oc != "" && e.store != nil {
		// Live: the headline moves the moment a step says what it found.
		if err := e.store.SetRunOutcome(context.WithoutCancel(ctx), runID, oc); err != nil {
			e.emit(ctx, runID, step.ID, "log", map[string]any{"message": "outcome: " + err.Error()})
		}
	}
	e.emit(ctx, runID, step.ID, "tool_result", map[string]any{
		"name": "run", "isError": exitCode != 0,
		"output": trim(stdout.String()+stderr.String(), maxEventOutput),
	})
	// A non-zero exit is reported in the output, not as an error: runOneStep
	// fails the step on it unless the step opted out (exitFailed).
	return out, nil
}

// runJudge executes a `judge` node: typed questions, no agent, no tools.
func (e *Engine) runJudge(ctx context.Context, runID uuid.UUID, step *workflow.Step, data workflow.TemplateData) (map[string]any, error) {
	e.setStep(ctx, runID, step.ID, model.StepPatch{
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
	e.setStep(ctx, runID, step.ID, model.StepPatch{Decision: mustJSON(rec)})
	return vals, nil
}

// isAgent reports whether a step actually runs an agent — the expensive kind.
func isAgent(s *workflow.Step) bool {
	return s.Run == "" && s.Judge == nil && s.Send == nil && s.Start == nil
}

// CallKey names a module call's directory: the call's id (the step id without
// the command's own step), made safe as one path segment. Steps of one call
// share it; two calls never do.
func CallKey(stepID string) string {
	call := stepID
	if i := strings.LastIndex(stepID, "."); i >= 0 {
		call = stepID[:i]
	}
	key := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, call)
	// Never a path step of its own: no "..", no leading dot.
	for strings.Contains(key, "..") {
		key = strings.ReplaceAll(key, "..", "_")
	}
	key = strings.TrimLeft(key, ".")
	if key == "" {
		key = "_"
	}
	return key
}
