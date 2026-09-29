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
	// redactions holds each run's sensitive values so no event stores one.
	redactions runSecrets

	cfg     config.Config
	store   Store
	blob    Artifacts
	defs    map[string]*workflow.Definition
	skills  *skills.Registry
	catalog *catalog.Catalog
	// priceTab is the model_prices table in memory (prices.go).
	priceTab priceTable
	// sourceSkips records workflow sources that did not load — a broken file in
	// one imported repository must not stop the platform booting, so it is
	// recorded and reported rather than fatal.
	sourceSkips []workflow.Skip
	// bundleRoots are the BUNDLE-SCOPED skill roots — one directory per pulled
	// bundle, holding exactly that bundle's carried skills. They are PREPENDED
	// to the machine's own roots, so skills.Load's existing first-root-wins
	// rule resolves each step's `skills:` to the carried copy and a same-named
	// machine skill is recorded in Shadowed rather than silently winning.
	//
	// The resolution rule is used, not bypassed: it is WHY bundling works.
	bundleRoots []string
	broker      *broker
	// mock is the in-process endpoint a `mock` provider points at, started on
	// first use. One per engine, on loopback.
	mock mockServer
	// classifierOpts overrides the judge backend; tests set the static one.
	classifierOpts *tn.ClassifierOptions
	// transport overrides the LLM HTTP transport (tests script it).
	transport http.RoundTripper
	// providerOverride, when set, runs every agent step on this registry
	// provider instead of the one it names (provider.go, UseProvider).
	providerOverride string
	// remote resolves a REMOTE `use:` — a git repository and a ref — into the
	// task a workflow expands. Nil ⇒ remote references are unavailable, which
	// is the one-person path: a workflow whose every `use:` is a bare task
	// name never reaches any of this.
	remote workflow.RemoteResolver

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
	e.loadPrices(context.Background())
	return e
}

// UseRemoteResolver wires remote `use:` resolution. The resolver materialises
// each fetched bundle's skills as a bundle-scoped skill root, which
// ReloadDefinitions then PREPENDS — the same first-root-wins arrangement
// `wfx pull` uses, not a second resolution order.
func (e *Engine) UseRemoteResolver(r workflow.RemoteResolver) { e.remote = r }

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
	reg := skills.LoadRoots(e.skillRoots()...)
	cat, err := catalog.Load(e.cfg.RegistriesPath, e.cfg.McpConfig)
	if err != nil {
		return err
	}
	// Every source, not just our own: a repository imported with ImportRepo
	// contributes the workflows in its `.wfx/workflows/`.
	//
	// A remote `use:` is resolved HERE, at load time, and the bundle it fetches
	// carries skills a step will name. Those roots do not exist until the fetch
	// has happened, so the load runs again once with the registry rebuilt over
	// them. The second pass is a cache hit by construction: it makes no network
	// call, and a load with no remote `use:` never takes it.
	var opts []workflow.LoadOption
	if e.remote != nil {
		opts = append(opts, workflow.WithRemoteResolver(e.remote))
	}
	defs, skips, err := workflow.LoadSources(e.Sources(), catalog.NewValidator(reg, cat), opts...)
	if e.adoptBundleRoots() {
		reg = skills.LoadRoots(e.skillRoots()...)
		defs, skips, err = workflow.LoadSources(e.Sources(), catalog.NewValidator(reg, cat), opts...)
	}
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.defs, e.skills, e.catalog, e.sourceSkips = defs, reg, cat, skips
	e.mu.Unlock()
	return nil
}

// adoptBundleRoots takes on any skill root the remote resolver materialised
// during the load just finished, and reports whether anything is new.
func (e *Engine) adoptBundleRoots() bool {
	type rooter interface{ Roots() []string }
	r, ok := e.remote.(rooter)
	if !ok {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	added := false
	have := map[string]bool{}
	for _, d := range e.bundleRoots {
		have[d] = true
	}
	for _, d := range r.Roots() {
		if have[d] {
			continue
		}
		e.bundleRoots = append([]string{d}, e.bundleRoots...)
		have[d] = true
		added = true
	}
	return added
}

// skillRoots is every root the registry is built from, bundle roots first and
// imported git skill sources LAST: importing a repository of skills never
// changes what an existing skill name resolves to (skillsources.go).
func (e *Engine) skillRoots() []skills.Root {
	e.mu.Lock()
	var roots []skills.Root
	for _, d := range e.bundleRoots {
		roots = append(roots, skills.Root{Dir: d})
	}
	e.mu.Unlock()
	for _, d := range skills.DefaultRoots(e.cfg.SkillsDir) {
		roots = append(roots, skills.Root{Dir: d})
	}
	return append(roots, e.skillSourceRoots()...)
}

// BundleRootDirFor is where a machine keeps its pulled bundles, derivable
// before an Engine exists — boot resolves remote references too.
func BundleRootDirFor(cfg config.Config) string {
	return filepath.Join(filepath.Dir(cfg.SkillsDir), "bundles")
}

// BundleRootDir is where pulled bundles are materialised: one directory per
// bundle, beside the platform's own skills rather than inside them, so a
// bundle's copy is never mistaken for a machine-wide install.
func (e *Engine) BundleRootDir() string { return BundleRootDirFor(e.cfg) }

// PrependSkillRoot registers a bundle-scoped skill root and rebuilds the
// registry. Called by the SERVER on a CLI-initiated publish or pull, never by
// a tool handed to a step — skills/platform.go's rule that a platform tool does
// not write is untouched by this change.
func (e *Engine) PrependSkillRoot(dir string) error {
	e.mu.Lock()
	for _, r := range e.bundleRoots {
		if r == dir {
			e.mu.Unlock()
			return e.ReloadDefinitions()
		}
	}
	e.bundleRoots = append([]string{dir}, e.bundleRoots...)
	e.mu.Unlock()
	return e.ReloadDefinitions()
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
	// Always the platform's own directory: a bundle pull or a copy lands HERE,
	// and must never overwrite a repository's file that shares its name.
	return e.SaveWorkflowIn("local", d)
}

// SaveWorkflowIn is SaveWorkflow into a named project — the repository's own
// `.wfx/workflows/`, which is where the builder is opened from.
//
// Before this, every save went to the platform's own directory: editing a
// repository's workflow in the builder quietly wrote a second copy into
// `local`, and the repository's file never changed.
func (e *Engine) SaveWorkflowIn(project string, d *workflow.Definition) (string, error) {
	dir, err := e.saveDir(project, d.Name)
	if err != nil {
		return "", err
	}

	// The host half of the mount rule, applied HERE rather than only when the
	// run starts. workflow.CheckMounts cannot do it — `~/.ssh` means a
	// different folder on every machine — but a save happens on the platform,
	// so the platform can and should answer for its own disk. Without this, a
	// workflow mounting /etc saved cleanly and failed at the first run, which
	// teaches the author nothing at the moment they could act on it.
	if err := e.checkMountHosts(d.Mount); err != nil {
		return "", err
	}
	path, err := workflow.Save(dir, d, e.validator())
	if err != nil {
		return "", err
	}
	return path, e.ReloadDefinitions()
}

// saveDir decides which directory a save writes into. See SaveWorkflowIn.
//
// With a project, the question is only whether that project has a directory:
// its own copy of the name (`project/name`, how the loader always keys it) is
// saved back in place, and a name it does not have yet is created there —
// even when another project has the same short name, because the loader keeps
// both reachable by their qualified names. Without one, an existing workflow
// goes back to its own source and a new one to the platform's directory.
func (e *Engine) saveDir(project, name string) (string, error) {
	home := project
	if home == "" {
		home = "local"
		if existing := e.Definitions()[name]; existing != nil && existing.Source != "" {
			home = existing.Source
		}
	}
	for _, src := range e.Sources() {
		if src.Name == home && src.Name != templatesSource {
			return src.Dir, nil
		}
	}
	return "", fmt.Errorf("no project named %q", home)
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
	// Before ANY destination — the store, the live stream, a worker's sink.
	payload = redactEvent(payload, e.redactions.get(runID))
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
	errMsg = redactText(errMsg, e.redactions.get(runID))
	if err := e.store.UpdateRun(context.WithoutCancel(ctx), runID, status, step, errMsg); err != nil {
		log.Printf("engine: update run: %v", err)
	}
	e.emit(ctx, runID, step, "run.status", map[string]any{"status": status, "step": step, "error": errMsg})
}

func (e *Engine) setStep(ctx context.Context, runID uuid.UUID, stepID string, p model.StepPatch) {
	if p.Error != nil {
		p.Error = str(redactText(*p.Error, e.redactions.get(runID)))
	}
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

// Actor is WHO resolved a pause (ADR 0021).
//
// There is no authentication yet — ADR 0017 is only proposed — so this is
// whatever the API is told, and it is recorded as a claim, not a verified
// identity. The point is that the SHAPE exists at every resolve path: when
// identity lands it is filled from the authenticated subject instead of the
// request body, and nothing downstream changes.
//
// It is REQUIRED. An empty actor is refused rather than defaulted, because
// "approved by nobody" is exactly the audit record this ADR exists to stop.
type Actor struct {
	// ID is the principal's identifier — today a name the caller asserts.
	ID string
	// Via is how the claim arrived: "api", "ui", "cli". Recorded with the
	// actor so a later audit can tell an unauthenticated era from an
	// authenticated one.
	Via string
	// Inferred marks an ID that NOBODY TYPED — one the client filled in from its
	// environment because no actor was given.
	//
	// It exists because the convenient default is a lie waiting to happen. `wfx`
	// falls back to `$USER@hostname`, which is right for a person at their own
	// terminal and wrong for an agent running the same command on their machine:
	// the approval is then recorded against a human who never saw it. The
	// platform cannot tell those two apart — the value is asserted, not
	// authenticated — so it records that it could not, rather than presenting a
	// guess as a decision.
	//
	// Only meaningful on the UNAUTHENTICATED path. An authenticated subject wins
	// over a claimed actor (see api.actorOf), and a subject is never inferred.
	Inferred bool
	// Authenticated is true when ID is an ADR 0017 subject rather than a claim.
	// Only an authenticated actor can be checked against a step's approvers:
	// a claimed name is exactly what a forger would type.
	Authenticated bool
	// Role is the authenticated subject's role, matched by `role:<name>`.
	Role string
}

func (a Actor) String() string {
	out := a.ID
	if a.Via != "" {
		out += " (via " + a.Via + ")"
	}
	if a.Inferred {
		// Said plainly in the audit record itself, because that string is what
		// somebody reads months later when they ask who approved this.
		out += " [inferred from the environment; nobody asserted it]"
	}
	return out
}

func (a Actor) validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("an actor is required: who is resolving this pause?")
	}
	return nil
}

// resolution is the audit patch: who, when, and what they decided.
func resolution(a Actor, resolution, reason string) model.StepPatch {
	now := time.Now().UTC()
	return model.StepPatch{
		ResolvedBy: str(a.String()), ResolvedAt: &now,
		Resolution: str(resolution), ResolutionReason: str(reason),
	}
}

// Answer.Data keys Resolve reads beyond toolnexus' own RelayOutputKey (the
// free-text answer to a question).
const (
	// AnswerInput carries a form's fields (map[string]any), merged into the run
	// input — what the /input endpoint sends.
	AnswerInput = "input"
	// AnswerReason carries a human's free-text reason for a refusal. It is kept
	// apart from Answer.Reason, which is toolnexus' closed vocabulary
	// (declined|cancelled|expired) and must stay machine-readable.
	AnswerReason = "reason"
)

// Resolve is THE resolve path (ADR 0021): every way a person answers a pause —
// approve, reject, answer a question, fill a form — arrives here as one
// tn.Answer from one Actor. That is what makes "who may answer" enforceable in
// one place rather than four, and why Ok/Reason are never thrown away.
//
// What the answer MEANS is decided by what the step is waiting on, not by the
// caller: an approval gate reads Ok as approve/reject; a question or a
// needs_input gate reads the payload.
func (e *Engine) Resolve(ctx context.Context, runID uuid.UUID, stepID string, ans tn.Answer, by Actor) error {
	if err := by.validate(); err != nil {
		return err
	}
	st, err := e.store.GetStep(ctx, runID, stepID)
	if err != nil {
		return err
	}
	if err := e.mayAnswer(runID, stepID, by); err != nil {
		return err
	}
	form, isForm := ans.Data[AnswerInput].(map[string]any)
	switch {
	case st.Status == "awaiting_approval":
		if ans.Ok {
			return e.approve(ctx, runID, stepID, by)
		}
		reason, _ := ans.Data[AnswerReason].(string)
		if reason == "" {
			reason = ans.Reason
		}
		return e.reject(ctx, runID, stepID, reason, by)
	case isForm:
		return e.provideInput(ctx, runID, stepID, form, by)
	case ans.Ok && ans.Data == nil:
		// A bare "yes" is an approval, and only an approval gate takes one.
		// Refused rather than read as an empty answer, which would silently
		// re-run a step nobody was asked about.
		return fmt.Errorf("step %s is %s, not awaiting_approval", stepID, st.Status)
	default:
		return e.answerQuestion(ctx, runID, stepID, ans, by)
	}
}

// mayAnswer enforces a step's `approvers:` (ADR 0021, "who may answer").
//
// The unauthenticated loopback is let through: there is no identity to check,
// only a claim, and refusing it would make approvers a config that breaks the
// one-person install. The claim is still recorded — and marked as a claim —
// by the audit patch, exactly as before.
func (e *Engine) mayAnswer(runID uuid.UUID, stepID string, by Actor) error {
	run, step := e.stepDef(runID, stepID)
	if step == nil || !by.Authenticated {
		return nil
	}
	if step.PreventSelfApproval && run.TriggeredBy != "" && run.TriggeredBy == by.ID {
		return fmt.Errorf("%w: step %s requires a different approver — %s started this run and may not answer it (prevent_self_approval)",
			ErrNotApprover, stepID, by.ID)
	}
	if len(step.Approvers) == 0 {
		return nil
	}
	for _, a := range step.Approvers {
		if a == by.ID || (by.Role != "" && a == "role:"+by.Role) {
			return nil
		}
	}
	return fmt.Errorf("%w: step %s may be answered only by %s; %s (role %q) is not one of them",
		ErrNotApprover, stepID, strings.Join(step.Approvers, ", "), by.ID, by.Role)
}

// ErrNotApprover is a refusal on WHO, not on the request: the API answers it
// with 403 rather than 400.
var ErrNotApprover = errors.New("not an approver")

// stepDef is a run and the definition of its step; the step is nil when the
// workflow has since been removed — in which case there is no policy left to
// enforce.
func (e *Engine) stepDef(runID uuid.UUID, stepID string) (*model.Run, *workflow.Step) {
	run, err := e.store.GetRun(context.Background(), runID)
	if err != nil {
		return nil, nil
	}
	def := e.defs[run.Workflow]
	if def == nil {
		return run, nil
	}
	_, s := def.Step(stepID)
	return run, s
}

// Approve unblocks a step that is awaiting approval and continues the run.
func (e *Engine) Approve(ctx context.Context, runID uuid.UUID, stepID string, by Actor) error {
	return e.Resolve(ctx, runID, stepID, tn.Answer{Ok: true}, by)
}

// Reject refuses an approval gate; the run is cancelled with the reason.
func (e *Engine) Reject(ctx context.Context, runID uuid.UUID, stepID, reason string, by Actor) error {
	return e.Resolve(ctx, runID, stepID,
		tn.Answer{Ok: false, Reason: "declined", Data: map[string]any{AnswerReason: reason}}, by)
}

// AnswerQuestion resolves a step that suspended on ask_human, or a needs_input
// gate, with a free-text answer (Data[tn.RelayOutputKey]).
func (e *Engine) AnswerQuestion(ctx context.Context, runID uuid.UUID, stepID string, ans tn.Answer, by Actor) error {
	return e.Resolve(ctx, runID, stepID, ans, by)
}

// ProvideInput merges a form into the run input and re-runs the step that
// asked — the run's current step.
func (e *Engine) ProvideInput(ctx context.Context, runID uuid.UUID, answers map[string]any, by Actor) error {
	if err := by.validate(); err != nil {
		return err
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if answers == nil {
		answers = map[string]any{}
	}
	return e.Resolve(ctx, runID, run.CurrentStep, tn.Answer{Ok: true, Data: map[string]any{AnswerInput: answers}}, by)
}

func (e *Engine) approve(ctx context.Context, runID uuid.UUID, stepID string, by Actor) error {
	p := resolution(by, "approved", "")
	p.Status = str("approved")
	e.setStep(ctx, runID, stepID, p)
	e.emit(ctx, runID, stepID, "log", map[string]any{"text": "approved by " + by.String()})
	// move the run off awaiting_approval synchronously, so a caller that reads
	// it straight back (the UI does) never sees the state it just cleared
	e.setRun(ctx, runID, "queued", stepID, "")
	e.Start(runID)
	return nil
}

func (e *Engine) reject(ctx context.Context, runID uuid.UUID, stepID, reason string, by Actor) error {
	p := resolution(by, "rejected", reason)
	p.Status = str("rejected")
	p.Error = str(reason)
	e.setStep(ctx, runID, stepID, p)
	e.emit(ctx, runID, stepID, "log", map[string]any{"text": "rejected by " + by.String() + ": " + reason})
	e.setRun(ctx, runID, "cancelled", stepID, "rejected: "+reason)
	return nil
}

// answerQuestion resolves a step that suspended on ask_human.
//
// It deliberately does NOT call Runtime.Resume: that replays the whole turn
// from the original prompt with an empty history, re-running tools and paying
// for the turn twice, and the runtime does not survive a process restart
// anyway (spikes/03). The step is our durability boundary, so the answer is
// folded into the run input and the step re-runs from its prompt — which is
// also why steps must be idempotent in effect.
func (e *Engine) answerQuestion(ctx context.Context, runID uuid.UUID, stepID string, ans tn.Answer, by Actor) error {
	st, err := e.store.GetStep(ctx, runID, stepID)
	if err != nil {
		return err
	}
	var req tn.Request
	switch {
	case len(st.Pending) > 0:
		if err := json.Unmarshal(st.Pending, &req); err != nil {
			return err
		}
	case st.Status == "needs_input":
		// A needs_input GATE parks the step with its rendered message and no
		// ask_human request. The message IS the question; without this the CLI
		// (`wfx answer`) had no way to resolve a gate-parked run at all.
		req = tn.Request{Kind: "input", Prompt: st.Error}
	default:
		return fmt.Errorf("step %s is not waiting on a question", stepID)
	}
	// A NOT-OK answer is a real outcome, not a missing one, and its Reason
	// tells a decline from a timeout — which a bare answer string could not
	// (toolnexus types.go:76-81). The step does not re-run: nobody answered it.
	if !ans.Ok {
		reason := ans.Reason
		if reason == "" {
			reason = "declined"
		}
		p := resolution(by, reason, req.Prompt)
		p.Status = str("declined")
		p.Error = str("question " + reason + ": " + req.Prompt)
		e.setStep(ctx, runID, stepID, p)
		e.emit(ctx, runID, stepID, "log", map[string]any{"text": "question " + reason + " by " + by.String()})
		status := "cancelled"
		if reason == "expired" {
			status = "failed"
		}
		e.setRun(ctx, runID, status, stepID, "question "+reason+" by "+by.String())
		return nil
	}
	answer, _ := ans.Data[tn.RelayOutputKey].(string)
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
	if len(st.Pending) == 0 {
		// A gate-parked step reads its person's reply the way the UI form
		// writes it: the bare reply in extra_context. `answers` also carries
		// the gate's message, and a gate that shows reply EXAMPLES ("approve
		// F3-S02 …") would read its own examples back as decisions.
		extra, _ := input["extra_context"].(string)
		input["extra_context"] = strings.TrimSpace(extra + "\n\n" + answer)
	}
	if err := e.store.UpdateRunInput(ctx, runID, mustJSON(input)); err != nil {
		return err
	}
	e.emit(ctx, runID, stepID, "log", map[string]any{"text": "answered by " + by.String() + ": " + req.Prompt})
	// The audit fact is written AFTER the retry: Retry resets the step row
	// (ResetStepsFrom), so recording first would wipe exactly what we came to
	// keep. The event above is the second, unerasable copy.
	if err := e.Retry(ctx, runID, stepID); err != nil {
		return err
	}
	e.setStep(ctx, runID, stepID, resolution(by, "answered", ""))
	return nil
}

// provideInput merges answers into the run input and re-runs from the step that asked.
//
// It is a resolve path like the others, so it names its actor and leaves the
// same audit fact on the step: a form answered from the UI is a decision
// somebody made, and "answered by nobody" is the record ADR 0021 exists to stop.
func (e *Engine) provideInput(ctx context.Context, runID uuid.UUID, stepID string, answers map[string]any, by Actor) error {
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
	e.emit(ctx, runID, stepID, "log", map[string]any{"text": "input provided by " + by.String()})
	// Written after the retry for the same reason as AnswerQuestion: Retry
	// resets the step row, and would erase an audit fact written before it.
	if err := e.Retry(ctx, runID, stepID); err != nil {
		return err
	}
	e.setStep(ctx, runID, stepID, resolution(by, "answered", ""))
	return nil
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
	// What every remote `use:` RESOLVED to, on this run's own event stream. The
	// COMMIT is recorded and the tag is not, because a tag can move and a
	// commit cannot — a rerun reads this, never the reference's ref.
	for _, pin := range def.RemotePins {
		e.emit(ctx, runID, "", "use.pinned", map[string]any{
			"reference": pin.Reference, "remote": pin.Remote,
			"commit": pin.Commit, "digest": pin.Digest,
		})
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

	workdir, err := e.prepareWorkspace(ctx, runID, withRepoDefault(input, def.RepoDir))
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
		// A guard that fails skips the step before its approval is asked for:
		// nobody should be asked to approve a branch that is not taken.
		guard := workflow.TemplateData{RunID: runID.String(), WorkDir: workdir, BaseRef: baseRef, Input: input, Steps: outputs}
		if e.skipUnlessGuarded(ctx, runID, step, guard) {
			continue
		}
		if step.RequiresApproval && st.Status != "approved" {
			e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("awaiting_approval")})
			e.setRun(ctx, runID, "awaiting_approval", step.ID, "")
			// The sequential path halts HERE rather than in runOneStep, so the
			// announcement has to be here too — a pause nobody is told about is
			// the failure ADR 0021 names.
			e.notifyPause(ctx, runID, step.ID, "approval",
				"step "+step.ID+" needs approval before it runs", nil)
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

// withRepoDefault makes a project's own repository the default a run acts on.
//
// RepoDir was documented as exactly that — "the default repo a run of it acts
// on" — and nothing read it: a project workflow with no repo_path input ran in
// an empty directory, so an agent asked to find a cause "in this repository"
// found no repository (reqsume-prod-watch, first rehearsal). The run still gets
// its own worktree unless the input opts out with isolate: false, so a project
// checkout is never worked in directly by default. An explicit repo_path or
// repo_url always wins.
func withRepoDefault(input map[string]any, repoDir string) map[string]any {
	if repoDir == "" {
		return input
	}
	if p, _ := input["repo_path"].(string); p != "" {
		return input
	}
	if u, _ := input["repo_url"].(string); u != "" {
		return input
	}
	out := make(map[string]any, len(input)+1)
	for k, v := range input {
		out[k] = v
	}
	out["repo_path"] = repoDir
	return out
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
