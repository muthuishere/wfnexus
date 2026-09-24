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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/secrets"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

type Engine struct {
	cfg     config.Config
	store   Store
	blob    Artifacts
	defs    map[string]*workflow.Definition
	skills  *skills.Registry
	catalog *catalog.Catalog
	// sourceSkips records workflow sources that did not load — a broken file in
	// one imported repository must not stop the platform booting, so it is
	// recorded and reported rather than fatal.
	sourceSkips []workflow.Skip
	broker      *broker
	// classifierOpts overrides the judge backend; tests set the static one.
	classifierOpts *tn.ClassifierOptions
	// transport overrides the LLM HTTP transport (tests script it).
	transport http.RoundTripper

	// secrets seals and opens the platform's env store. Nil when no key could
	// be loaded, which makes the store unavailable rather than plaintext.
	secrets     model.Sealer
	secretsFrom string

	// sink replaces the event store on a worker engine, where there is no
	// database: every event goes here to be posted back to the platform.
	sink func(kind string, payload any)

	mu      sync.Mutex
	running map[uuid.UUID]context.CancelFunc
	// attached is what each live run's `mount:` turned into on this machine:
	// the extra roots its agents may reach, and the WFX_MOUNT_* variables its
	// steps get. Held per run rather than threaded through every execution
	// path, because a mount belongs to the RUN's workspace and every step of
	// that run sees the same one.
	attached map[uuid.UUID]mountState
	// slots bounds concurrently EXECUTING runs. A run beyond the limit holds its
	// goroutine and stays queued, so the cap is on machine load, not on
	// accepting work.
	slots chan struct{}
}

func New(cfg config.Config, st Store, bl Artifacts, defs map[string]*workflow.Definition, reg *skills.Registry, cat *catalog.Catalog) *Engine {
	if cat == nil {
		cat, _ = catalog.Load("", "")
	}
	limit := cfg.MaxConcurrentRuns
	if limit < 1 {
		limit = 1
	}
	e := &Engine{
		cfg: cfg, store: st, blob: bl, defs: defs, skills: reg, catalog: cat,
		broker: newBroker(), running: map[uuid.UUID]context.CancelFunc{},
		attached: map[uuid.UUID]mountState{},
		slots:    make(chan struct{}, limit),
	}
	// The key is loaded once, at boot. A failure is not fatal — a platform with
	// no stored secrets works perfectly well — but it is reported, because a
	// step that expected one will otherwise fail later and further away.
	if box, from, err := secrets.Load(cfg.SecretKeyEnv, cfg.SecretKeyPath); err == nil {
		e.secrets, e.secretsFrom = box, from
	} else {
		log.Printf("engine: env store unavailable: %v", err)
	}
	return e
}

// UseTransport overrides the LLM HTTP transport (tests script the wire).
func (e *Engine) UseTransport(rt http.RoundTripper) { e.transport = rt }

// UseClassifier overrides the judge backend (tests use the static one, which
// needs no network and never guesses at an unrecorded state).
func (e *Engine) UseClassifier(opts tn.ClassifierOptions) { e.classifierOpts = &opts }

// Skills is the registry backing every step's skill allowlist.
func (e *Engine) Skills() *skills.Registry { return e.skills }

// Catalog is the provider / classifier / MCP registry set.
func (e *Engine) Catalog() *catalog.Catalog { return e.catalog }

// SourceSkips lists sources or workflows that failed to load.
func (e *Engine) SourceSkips() []workflow.Skip {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sourceSkips
}

// validator is every registry a workflow can name, as one value.
func (e *Engine) validator() workflow.Catalog {
	e.mu.Lock()
	defer e.mu.Unlock()
	return catalog.NewValidator(e.skills, e.catalog)
}

func (e *Engine) Definitions() map[string]*workflow.Definition { return e.defs }

// ReloadDefinitions re-reads the skill registry and the workflows dir, so a new
// skill and the step that uses it land in one hot reload. Workflows are
// validated against the fresh registry; a bad reload changes nothing.
func (e *Engine) ReloadDefinitions() error {
	reg := skills.Load(skills.DefaultRoots(e.cfg.SkillsDir)...)
	cat, err := catalog.Load(e.cfg.RegistriesPath, e.cfg.McpConfig)
	if err != nil {
		return err
	}
	// Every source, not just our own: a repository imported with ImportRepo
	// contributes the workflows in its `.wfx/workflows/`.
	defs, skips, err := workflow.LoadSources(e.Sources(), catalog.NewValidator(reg, cat))
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.defs, e.skills, e.catalog, e.sourceSkips = defs, reg, cat, skips
	e.mu.Unlock()
	return nil
}

// CheckWorkflow is SaveWorkflow without the write.
func (e *Engine) CheckWorkflow(d *workflow.Definition) error {
	if err := workflow.Check(d, e.validator()); err != nil {
		return err
	}
	return checkSchemas(d)
}

// checkSchemas compiles every schema the definition declares, with the SAME
// compiler the run loop uses.
//
// The loader never did this, so `type: nonsense` or `required: "x"` passed
// validation and became a `submit_output` tool that no submission could ever
// satisfy — a run that fails on the first turn, for a mistake that was on
// screen while it was being authored. It belongs here rather than in
// workflow.Check because the compiler lives in this package.
func checkSchemas(d *workflow.Definition) error {
	if len(d.InputSchema) > 0 {
		if _, err := compileSchema("input_schema", d.InputSchema); err != nil {
			return fmt.Errorf("input_schema is not a valid JSON Schema: %w", err)
		}
	}
	for _, s := range d.Steps {
		if len(s.OutputSchema) == 0 {
			continue
		}
		if _, err := compileSchema("output_schema", s.OutputSchema); err != nil {
			return fmt.Errorf("step %q: output_schema is not a valid JSON Schema: %w", s.ID, err)
		}
	}
	return nil
}

// McpServers lists the server names a step may be granted, so an authoring UI
// offers a choice instead of free text whose typo surfaces at run time.
func (e *Engine) McpServers() []string {
	raw, err := os.ReadFile(e.cfg.McpConfig)
	if err != nil {
		return []string{}
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return []string{}
	}
	servers, _ := all["mcpServers"].(map[string]any)
	out := make([]string, 0, len(servers))
	for name := range servers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Models is the model catalog an authoring UI offers: the configured default
// first, then anything WFX_MODELS lists.
func (e *Engine) Models() []string {
	out := []string{e.cfg.Model}
	for _, m := range strings.Split(os.Getenv("WFX_MODELS"), ",") {
		if m = strings.TrimSpace(m); m != "" && m != e.cfg.Model {
			out = append(out, m)
		}
	}
	return out
}

// SaveWorkflow validates and persists an authored definition, then reloads so
// the new version is live without a restart. Validation happens on a temporary
// copy, so a rejected definition never lands on disk.
func (e *Engine) SaveWorkflow(d *workflow.Definition) (string, error) {
	// The host half of the mount rule, applied HERE rather than only when the
	// run starts. workflow.CheckMounts cannot do it — `~/.ssh` means a
	// different folder on every machine — but a save happens on the platform,
	// so the platform can and should answer for its own disk. Without this, a
	// workflow mounting /etc saved cleanly and failed at the first run, which
	// teaches the author nothing at the moment they could act on it.
	if err := e.checkMountHosts(d.Mount); err != nil {
		return "", err
	}
	path, err := workflow.Save(e.cfg.WorkflowsDir, d, e.validator())
	if err != nil {
		return "", err
	}
	return path, e.ReloadDefinitions()
}

// SaveProvider and friends write one registry entry and reload the catalog, so
// a workflow can name it immediately. They go through the loader's own
// validation (catalog/save.go) — the API cannot accept an entry the loader
// would skip.
func (e *Engine) SaveProvider(p catalog.Provider) error {
	if err := catalog.SaveProvider(e.cfg.RegistriesPath, p); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

func (e *Engine) SaveClassifier(c catalog.Classifier) error {
	if err := catalog.SaveClassifier(e.cfg.RegistriesPath, c); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

// DeleteProvider removes an entry. A workflow still naming it then FAILS TO
// LOAD, which is the loud outcome and the right one — the alternative is a step
// quietly running on a different model.
func (e *Engine) DeleteProvider(name string) error {
	if err := catalog.DeleteProvider(e.cfg.RegistriesPath, name); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

func (e *Engine) DeleteClassifier(name string) error {
	if err := catalog.DeleteClassifier(e.cfg.RegistriesPath, name); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

// DeleteWorkflow removes a workflow and reloads. Runs already recorded against
// it keep their history; only new runs are refused.
func (e *Engine) DeleteWorkflow(name string) error {
	if err := workflow.Delete(e.cfg.WorkflowsDir, name); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

func (e *Engine) Subscribe(runID uuid.UUID) (<-chan *model.Event, func()) {
	return e.broker.Subscribe(runID)
}

func (e *Engine) emit(ctx context.Context, runID uuid.UUID, stepID, kind string, payload any) {
	// A worker has no event table. Its activity is posted back to the platform,
	// which appends it to the run's log — so the live view of a step running on
	// somebody's Windows box is the same view as one running here.
	if e.store == nil {
		if e.sink != nil {
			e.sink(kind, map[string]any{"stepId": stepID, "kind": kind, "payload": payload})
		}
		return
	}
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

func (e *Engine) setStep(ctx context.Context, runID uuid.UUID, stepID string, p model.StepPatch) {
	if e.store == nil {
		// The platform owns the step row; the worker only reports what happened.
		if p.Status != nil {
			e.emit(ctx, runID, stepID, "step.status", map[string]any{"status": *p.Status, "error": deref(p.Error)})
		}
		return
	}
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
		// Wait for a slot. The run is already marked queued, so the UI shows it
		// waiting rather than silently doing nothing.
		select {
		case e.slots <- struct{}{}:
			defer func() { <-e.slots }()
		case <-ctx.Done():
			return
		}
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
	e.setStep(ctx, runID, stepID, model.StepPatch{Status: str("approved")})
	// move the run off awaiting_approval synchronously, so a caller that reads
	// it straight back (the UI does) never sees the state it just cleared
	e.setRun(ctx, runID, "queued", stepID, "")
	e.Start(runID)
	return nil
}

func (e *Engine) Reject(ctx context.Context, runID uuid.UUID, stepID, reason string) error {
	e.setStep(ctx, runID, stepID, model.StepPatch{Status: str("rejected"), Error: str(reason)})
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
	// The workspace is only half of what a run may need. `mount:` attaches the
	// folders the workflow declared, and the files beside the workflow on disk
	// are staged in — both BEFORE the first step, so step one can already say
	// `node run.js` or read from the mount.
	if err := e.prepareAttachments(ctx, runID, def, workdir); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	defer e.releaseAttachments(runID)
	// the base is recorded once per run, so a resume does not re-anchor diffs
	baseRef := run.BaseRef
	if baseRef == "" {
		if baseRef = gitRev(ctx, workdir); baseRef != "" {
			if err := e.store.SetBaseRef(ctx, runID, baseRef); err != nil {
				log.Printf("engine: set base ref: %v", err)
			}
		}
	}

	// A workflow that declares dependencies runs as a DAG, concurrently; one
	// that does not runs exactly as it always has.
	// Three ways to order a workflow, in increasing autonomy: a derived plan,
	// a hand-written DAG, or plain sequence.
	if def.IsPlanned() {
		return e.runPlanned(ctx, runID, def, input, workdir, baseRef)
	}
	if def.IsDAG() {
		return e.runDAG(ctx, runID, def, input, workdir, baseRef)
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
			e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("awaiting_approval")})
			e.setRun(ctx, runID, "awaiting_approval", step.ID, "")
			return nil
		}
		data := workflow.TemplateData{RunID: runID.String(), WorkDir: workdir, BaseRef: baseRef, Input: input, Steps: outputs}
		out, outcome := e.runOneStep(ctx, runID, def, step, data)
		if outcome == stepHalted {
			return nil
		}
		if outcome == stepDone {
			outputs[step.ID] = out
		}

		// needs_input and fail gates were already applied by runOneStep; only
		// skip_to remains, and it exists only on the sequential path — a forward
		// jump has no meaning once steps run concurrently, so a DAG refuses it
		// at load time.
		next := i + 1
		for _, g := range step.Gates {
			if g.Action != "skip_to" || !gateHit(g, out) {
				continue
			}
			pos, _ := def.Step(g.SkipTo)
			if pos < 0 {
				return fmt.Errorf("gate skip_to unknown step %q", g.SkipTo)
			}
			e.skipRange(ctx, runID, def, i+1, pos)
			next = pos
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
			e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("needs_input"), Error: str(msg)})
			e.setRun(ctx, runID, "needs_input", step.ID, msg)
			return -1, true
		case "fail":
			e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("failed"), Error: str(msg), FinishedAt: now()})
			e.setRun(ctx, runID, "failed", step.ID, msg)
			return -1, true
		case "skip_to":
			pos, _ := def.Step(g.SkipTo)
			if pos >= 0 {
				e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("skipped"), FinishedAt: now()})
				return pos, false
			}
		}
	}
	return -1, false
}

// skipRange marks the steps between two positions skipped.
func (e *Engine) skipRange(ctx context.Context, runID uuid.UUID, def *workflow.Definition, from, to int) {
	for j := from; j < to && j < len(def.Steps); j++ {
		e.setStep(ctx, runID, def.Steps[j].ID, model.StepPatch{Status: str("skipped")})
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
