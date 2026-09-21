// Package engine executes workflow runs: one toolnexus agent per step, with
// schema-validated output, gates, approvals and durable per-step state.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/blob"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/config"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/skills"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/store"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

type Engine struct {
	cfg    config.Config
	store  *store.Store
	blob   *blob.Blob
	defs   map[string]*workflow.Definition
	skills *skills.Registry
	broker *broker
	// classifierOpts overrides the judge backend; tests set the static one.
	classifierOpts *tn.ClassifierOptions
	// transport overrides the LLM HTTP transport (tests script it).
	transport http.RoundTripper

	mu      sync.Mutex
	running map[uuid.UUID]context.CancelFunc
}

func New(cfg config.Config, st *store.Store, bl *blob.Blob, defs map[string]*workflow.Definition, reg *skills.Registry) *Engine {
	return &Engine{cfg: cfg, store: st, blob: bl, defs: defs, skills: reg, broker: newBroker(), running: map[uuid.UUID]context.CancelFunc{}}
}

// UseTransport overrides the LLM HTTP transport (tests script the wire).
func (e *Engine) UseTransport(rt http.RoundTripper) { e.transport = rt }

// UseClassifier overrides the judge backend (tests use the static one, which
// needs no network and never guesses at an unrecorded state).
func (e *Engine) UseClassifier(opts tn.ClassifierOptions) { e.classifierOpts = &opts }

// Skills is the registry backing every step's skill allowlist.
func (e *Engine) Skills() *skills.Registry { return e.skills }

func (e *Engine) Definitions() map[string]*workflow.Definition { return e.defs }

// ReloadDefinitions re-reads the skill registry and the workflows dir, so a new
// skill and the step that uses it land in one hot reload. Workflows are
// validated against the fresh registry; a bad reload changes nothing.
func (e *Engine) ReloadDefinitions() error {
	reg := skills.Load(skills.DefaultRoots(e.cfg.SkillsDir)...)
	defs, err := workflow.LoadDir(e.cfg.WorkflowsDir, reg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.defs, e.skills = defs, reg
	e.mu.Unlock()
	return nil
}

func (e *Engine) Subscribe(runID uuid.UUID) (<-chan *store.Event, func()) {
	return e.broker.Subscribe(runID)
}

func (e *Engine) emit(ctx context.Context, runID uuid.UUID, stepID, kind string, payload any) {
	ev, err := e.store.AppendEvent(context.WithoutCancel(ctx), runID, stepID, kind, payload)
	if err != nil {
		log.Printf("engine: append event: %v", err)
		return
	}
	e.broker.Publish(ev)
}

func (e *Engine) setRun(ctx context.Context, runID uuid.UUID, status, step, errMsg string) {
	if err := e.store.UpdateRun(context.WithoutCancel(ctx), runID, status, step, errMsg); err != nil {
		log.Printf("engine: update run: %v", err)
	}
	e.emit(ctx, runID, step, "run.status", map[string]any{"status": status, "step": step, "error": errMsg})
}

func (e *Engine) setStep(ctx context.Context, runID uuid.UUID, stepID string, p store.StepPatch) {
	if err := e.store.PatchStep(context.WithoutCancel(ctx), runID, stepID, p); err != nil {
		log.Printf("engine: patch step: %v", err)
	}
	if p.Status != nil {
		e.emit(ctx, runID, stepID, "step.status", map[string]any{"status": *p.Status, "error": deref(p.Error)})
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func str(s string) *string { return &s }
func intp(i int) *int      { return &i }
func now() *time.Time      { t := time.Now(); return &t }

// Start resumes a run in the background. Idempotent: a run already executing is left alone.
func (e *Engine) Start(runID uuid.UUID) {
	e.mu.Lock()
	if _, ok := e.running[runID]; ok {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.running[runID] = cancel
	e.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			e.mu.Lock()
			delete(e.running, runID)
			e.mu.Unlock()
		}()
		if err := e.resume(ctx, runID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("engine: run %s: %v", runID, err)
			e.setRun(ctx, runID, "failed", "", err.Error())
		}
	}()
}

func (e *Engine) Cancel(ctx context.Context, runID uuid.UUID) {
	e.mu.Lock()
	cancel := e.running[runID]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.setRun(ctx, runID, "cancelled", "", "")
}

// Approve unblocks a step that is awaiting approval and continues the run.
func (e *Engine) Approve(ctx context.Context, runID uuid.UUID, stepID string) error {
	st, err := e.store.GetStep(ctx, runID, stepID)
	if err != nil {
		return err
	}
	if st.Status != "awaiting_approval" {
		return fmt.Errorf("step %s is %s, not awaiting_approval", stepID, st.Status)
	}
	e.setStep(ctx, runID, stepID, store.StepPatch{Status: str("approved")})
	// move the run off awaiting_approval synchronously, so a caller that reads
	// it straight back (the UI does) never sees the state it just cleared
	e.setRun(ctx, runID, "queued", stepID, "")
	e.Start(runID)
	return nil
}

func (e *Engine) Reject(ctx context.Context, runID uuid.UUID, stepID, reason string) error {
	e.setStep(ctx, runID, stepID, store.StepPatch{Status: str("rejected"), Error: str(reason)})
	e.setRun(ctx, runID, "cancelled", stepID, "rejected: "+reason)
	return nil
}

// AnswerQuestion resolves a step that suspended on ask_human.
//
// It deliberately does NOT call Runtime.Resume: that replays the whole turn
// from the original prompt with an empty history, re-running tools and paying
// for the turn twice, and the runtime does not survive a process restart
// anyway (spikes/03). The step is our durability boundary, so the answer is
// folded into the run input and the step re-runs from its prompt — which is
// also why steps must be idempotent in effect.
func (e *Engine) AnswerQuestion(ctx context.Context, runID uuid.UUID, stepID, answer string) error {
	st, err := e.store.GetStep(ctx, runID, stepID)
	if err != nil {
		return err
	}
	if len(st.Pending) == 0 {
		return fmt.Errorf("step %s is not waiting on a question", stepID)
	}
	var req tn.Request
	if err := json.Unmarshal(st.Pending, &req); err != nil {
		return err
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	var input map[string]any
	_ = json.Unmarshal(run.Input, &input)
	if input == nil {
		input = map[string]any{}
	}
	prior, _ := input["answers"].(string)
	input["answers"] = strings.TrimSpace(prior + "\n\nQ: " + req.Prompt + "\nA: " + answer)
	if err := e.store.UpdateRunInput(ctx, runID, mustJSON(input)); err != nil {
		return err
	}
	e.emit(ctx, runID, stepID, "log", map[string]any{"text": "operator answered: " + req.Prompt})
	return e.Retry(ctx, runID, stepID)
}

// ProvideInput merges answers into the run input and re-runs from the step that asked.
func (e *Engine) ProvideInput(ctx context.Context, runID uuid.UUID, answers map[string]any) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	var input map[string]any
	_ = json.Unmarshal(run.Input, &input)
	if input == nil {
		input = map[string]any{}
	}
	for k, v := range answers {
		input[k] = v
	}
	if err := e.store.UpdateRunInput(ctx, runID, mustJSON(input)); err != nil {
		return err
	}
	return e.Retry(ctx, runID, run.CurrentStep)
}

// Retry re-runs from stepID (inclusive), discarding later step state.
func (e *Engine) Retry(ctx context.Context, runID uuid.UUID, stepID string) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	def := e.defs[run.Workflow]
	if def == nil {
		return fmt.Errorf("unknown workflow %q", run.Workflow)
	}
	pos, _ := def.Step(stepID)
	if pos < 0 {
		return fmt.Errorf("unknown step %q", stepID)
	}
	if err := e.store.ResetStepsFrom(ctx, runID, pos); err != nil {
		return err
	}
	e.setRun(ctx, runID, "queued", stepID, "")
	e.Start(runID)
	return nil
}

// ---- the run loop ----

func (e *Engine) resume(ctx context.Context, runID uuid.UUID) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	def := e.defs[run.Workflow]
	if def == nil {
		return fmt.Errorf("unknown workflow %q", run.Workflow)
	}
	for i, s := range def.Steps {
		if err := e.store.EnsureStep(ctx, runID, s.ID, i); err != nil {
			return err
		}
	}
	var input map[string]any
	_ = json.Unmarshal(run.Input, &input)
	if input == nil {
		input = map[string]any{}
	}

	workdir, err := e.prepareWorkspace(ctx, runID, input)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	// the base is recorded once per run, so a resume does not re-anchor diffs
	baseRef := run.BaseRef
	if baseRef == "" {
		if baseRef = gitRev(ctx, workdir); baseRef != "" {
			if err := e.store.SetBaseRef(ctx, runID, baseRef); err != nil {
				log.Printf("engine: set base ref: %v", err)
			}
		}
	}

	steps, err := e.store.ListSteps(ctx, runID)
	if err != nil {
		return err
	}
	outputs := map[string]any{}
	start := 0
	for i, st := range steps {
		if st.Status == "done" || st.Status == "skipped" {
			if len(st.Output) > 0 {
				var o any
				_ = json.Unmarshal(st.Output, &o)
				outputs[st.StepID] = o
			}
			start = i + 1
			continue
		}
		break
	}

	for i := start; i < len(def.Steps); i++ {
		step := &def.Steps[i]
		st, err := e.store.GetStep(ctx, runID, step.ID)
		if err != nil {
			return err
		}
		if step.RequiresApproval && st.Status != "approved" {
			e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("awaiting_approval")})
			e.setRun(ctx, runID, "awaiting_approval", step.ID, "")
			return nil
		}
		e.setRun(ctx, runID, "running", step.ID, "")
		data := workflow.TemplateData{RunID: runID.String(), WorkDir: workdir, BaseRef: baseRef, Input: input, Steps: outputs}

		// The judge runs BEFORE the agent: typed questions on a small model can
		// route or halt for a fraction of a cent instead of a full agent run.
		rec, vals, err := e.decide(ctx, runID, step, data)
		if err != nil {
			e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("failed"), Error: str(err.Error()), FinishedAt: now()})
			e.setRun(ctx, runID, "failed", step.ID, err.Error())
			return nil
		}
		if rec != nil {
			e.setStep(ctx, runID, step.ID, store.StepPatch{Decision: mustJSON(rec)})
			data.Decide = vals
			if jump, stop := e.applyDecideGates(ctx, runID, def, step, vals, data); stop {
				return nil
			} else if jump >= 0 {
				e.skipRange(ctx, runID, def, i+1, jump)
				i = jump - 1
				continue
			}
		}

		res, err := e.executeStep(ctx, runID, def, step, data)
		if err != nil {
			e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("failed"), Error: str(err.Error()), FinishedAt: now()})
			e.setRun(ctx, runID, "failed", step.ID, err.Error())
			return nil
		}
		if res.Pending != nil {
			// The agent asked a question. The Request is plain data, so the run
			// parks here and the answer may arrive in another process, later.
			e.setStep(ctx, runID, step.ID, store.StepPatch{
				Status: str("needs_input"), Pending: mustJSON(res.Pending), Error: str(res.Pending.Prompt),
			})
			e.setRun(ctx, runID, "needs_input", step.ID, res.Pending.Prompt)
			return nil
		}
		out := res.Output
		outputs[step.ID] = out
		e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("done"), Output: mustJSON(out), FinishedAt: now()})

		// gates
		next := i + 1
		for _, g := range step.Gates {
			if !gateHit(g, out) {
				continue
			}
			data.Output = out
			msg, _ := workflow.Render(g.Message, data)
			switch g.Action {
			case "needs_input":
				e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("needs_input"), Error: str(msg)})
				e.setRun(ctx, runID, "needs_input", step.ID, msg)
				return nil
			case "fail":
				e.setRun(ctx, runID, "failed", step.ID, msg)
				return nil
			case "skip_to":
				pos, _ := def.Step(g.SkipTo)
				if pos < 0 {
					return fmt.Errorf("gate skip_to unknown step %q", g.SkipTo)
				}
				for j := i + 1; j < pos; j++ {
					e.setStep(ctx, runID, def.Steps[j].ID, store.StepPatch{Status: str("skipped")})
				}
				next = pos
			}
			break
		}
		i = next - 1
	}
	e.setRun(ctx, runID, "done", "", "")
	return nil
}

// applyDecideGates branches on the judge's answers. It returns the index to
// jump to (or -1), and whether the run stopped here.
func (e *Engine) applyDecideGates(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, vals map[string]any, data workflow.TemplateData) (int, bool) {
	if step.Decide == nil {
		return -1, false
	}
	for _, g := range step.Decide.Gates {
		if !decideGate(g, vals) {
			continue
		}
		msg, _ := workflow.Render(g.Message, data)
		switch g.Action {
		case "needs_input":
			e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("needs_input"), Error: str(msg)})
			e.setRun(ctx, runID, "needs_input", step.ID, msg)
			return -1, true
		case "fail":
			e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("failed"), Error: str(msg), FinishedAt: now()})
			e.setRun(ctx, runID, "failed", step.ID, msg)
			return -1, true
		case "skip_to":
			pos, _ := def.Step(g.SkipTo)
			if pos >= 0 {
				e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("skipped"), FinishedAt: now()})
				return pos, false
			}
		}
	}
	return -1, false
}

// skipRange marks the steps between two positions skipped.
func (e *Engine) skipRange(ctx context.Context, runID uuid.UUID, def *workflow.Definition, from, to int) {
	for j := from; j < to && j < len(def.Steps); j++ {
		e.setStep(ctx, runID, def.Steps[j].ID, store.StepPatch{Status: str("skipped")})
	}
}

func gateHit(g workflow.Gate, out map[string]any) bool {
	v, ok := out[g.Field]
	if !ok {
		return false
	}
	// json numbers decode as float64; compare via JSON text to be type-lenient
	return string(mustJSON(v)) == string(mustJSON(g.Equals))
}

// prepareWorkspace returns the repo directory for this run: input.repo_path is
// used as-is; input.repo_url is cloned once under the runtime dir.
func (e *Engine) prepareWorkspace(ctx context.Context, runID uuid.UUID, input map[string]any) (string, error) {
	if p, _ := input["repo_path"].(string); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
		if isolate, ok := input["isolate"].(bool); ok && !isolate {
			return p, nil // explicit opt-out: the agent works in the user's checkout
		}
		return e.worktree(ctx, runID, p, input)
	}
	dir := filepath.Join(e.cfg.WorkDir, runID.String(), "repo")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	url, _ := input["repo_url"].(string)
	if url == "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	args := []string{"clone", "--depth", "50"}
	if b, _ := input["base_branch"].(string); b != "" {
		args = append(args, "--branch", b)
	}
	args = append(args, url, dir)
	e.emit(ctx, runID, "", "log", map[string]any{"text": "git " + strings.Join(args, " ")})
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone: %v: %s", err, out)
	}
	return dir, nil
}

// worktree gives each run its own git worktree of a local repo, so concurrent
// runs against the same repository never fight over the index or HEAD. Falls
// back to the original path when the repo does not support worktrees.
func (e *Engine) worktree(ctx context.Context, runID uuid.UUID, repo string, input map[string]any) (string, error) {
	dir := filepath.Join(e.cfg.WorkDir, runID.String(), "worktree")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	base, _ := input["base_branch"].(string)
	if base == "" {
		base = "HEAD"
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	// detached worktree: the run branches from base itself, never moving the parent's HEAD
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "worktree", "add", "--detach", dir, base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.emit(ctx, runID, "", "log", map[string]any{"text": fmt.Sprintf("git worktree unavailable (%s), using the repo directly: %s", err, bytes.TrimSpace(out))})
		return repo, nil
	}
	e.emit(ctx, runID, "", "log", map[string]any{"text": "worktree " + dir + " from " + base})
	return dir, nil
}

// gitRev resolves HEAD in dir; "" when it is not a git repo. Recorded once per
// resume so every step's diff artifact is measured from the same point.
func gitRev(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
