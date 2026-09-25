package engine

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/planner"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A dry run answers "would this workflow actually work here?" without calling a
// model, cloning a repository, or writing a single file.
//
// It exists because validation is not the same question. Validation asks
// whether the FILE is well formed; a dry run asks whether THIS MACHINE can run
// it. Almost every failure this project has actually had was the second kind
// and was invisible until a real run burned turns to find it:
//
//   - a step named a provider whose key variable was not set
//   - a prompt template referenced a step id with a hyphen and would not render
//   - a `run:` step needed a shell the machine did not have
//   - a goal-planned workflow's goal was produced by no step
//   - a step's template referenced `.Steps.later` — a step that runs AFTER it
//
// Each is certain before anything runs, and each costs nothing to check. So
// the dry run is the cheap gate in front of the expensive one — and it is the
// tool the workflow-authoring agent uses to check its own work.
type DryRun struct {
	Workflow string         `json:"workflow"`
	OK       bool           `json:"ok"`
	Shape    string         `json:"shape"`
	Waves    [][]string     `json:"waves"`
	Steps    []DryRunStep   `json:"steps"`
	Problems []DryProblem   `json:"problems"`
	Cost     DryRunCost     `json:"cost"`
	Input    map[string]any `json:"input"`
}

// DryRunStep is what one step would do.
type DryRunStep struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Shell    string `json:"shell,omitempty"`
	// RunsOn is the label this step is placed on, and Workers is how many
	// machines currently hold it — "where would this actually execute".
	RunsOn  string `json:"runsOn,omitempty"`
	Workers int    `json:"workers,omitempty"`
	// Env are the variable NAMES this step reads from the environment. Names
	// only: a dry run has to be safe to paste into an issue.
	Env      []string `json:"env,omitempty"`
	Skills   []string `json:"skills,omitempty"`
	Tools    []string `json:"tools,omitempty"`
	MaxTurns int      `json:"maxTurns,omitempty"`
	// Prompt is the template ACTUALLY RENDERED against the sample input, so an
	// author sees what the agent would read rather than what they typed.
	Prompt string `json:"prompt,omitempty"`
	Wave   int    `json:"wave"`
}

// DryProblem is one thing that would go wrong, and where.
type DryProblem struct {
	Step    string `json:"step,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	// Fatal marks something that stops the run, as opposed to something that
	// merely costs more or reads oddly.
	Fatal bool `json:"fatal"`
}

// DryRunCost is the CEILING, not an estimate. Every step's budget is declared,
// so the worst case is arithmetic rather than a guess — and the worst case is
// the number worth knowing before starting something.
type DryRunCost struct {
	MaxTurns     int   `json:"maxTurns"`
	MaxToolCalls int64 `json:"maxToolCalls"`
	MaxWallSec   int   `json:"maxWallSec"`
	AgentSteps   int   `json:"agentSteps"`
	FreeSteps    int   `json:"freeSteps"`
}

// DryRunWorkflow checks a loaded workflow against this machine.
func (e *Engine) DryRunWorkflow(name string, input map[string]any) (*DryRun, error) {
	def := e.Definitions()[name]
	if def == nil {
		// A workflow that did not LOAD is not an unknown workflow — it is a
		// workflow with a problem, and a dry run is where a problem is
		// supposed to surface. An unresolvable remote `use:` is the case this
		// exists for: the whole static-pass claim is that it fails HERE and
		// not an hour into a run.
		if why, ok := e.loadFailure(name); ok {
			return &DryRun{
				Workflow: name,
				OK:       false,
				Problems: []DryProblem{{Message: why, Fatal: true}},
			}, nil
		}
		return nil, fmt.Errorf("workflow %q is not loaded", name)
	}
	return e.DryRunDefinition(def, input), nil
}

// loadFailure finds the recorded reason a named workflow is not loaded. The
// loader reports `"<workflow>: ..."`, and a source's skip carries that text
// verbatim.
func (e *Engine) loadFailure(name string) (string, bool) {
	short := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		short = name[i+1:]
	}
	for _, sk := range e.SourceSkips() {
		if strings.HasPrefix(sk.Reason, short+":") || strings.Contains(sk.Reason, " "+short+":") {
			return sk.Reason, true
		}
	}
	return "", false
}

// DryRunDefinition checks a definition that may not be saved anywhere — which
// is how the authoring agent checks a draft before proposing it.
func (e *Engine) DryRunDefinition(def *workflow.Definition, input map[string]any) *DryRun {
	out := &DryRun{Workflow: def.Name, OK: true}

	// Sample input: what was supplied, then the schema's declared defaults, then
	// an obvious placeholder per required property. A dry run must not fail for
	// the want of an input a real run would collect from a form.
	sample := sampleInput(def, input)
	out.Input = sample

	fail := func(p DryProblem) {
		if p.Fatal {
			out.OK = false
		}
		out.Problems = append(out.Problems, p)
	}

	// What the four state namespaces would hold, so a reference to a key
	// nothing has written and nothing will write is reported before the run.
	state := e.dryState(def)

	waves, shape, err := e.dryWaves(def, sample)
	out.Shape, out.Waves = shape, waves
	if err != nil {
		fail(DryProblem{Message: err.Error(), Fatal: true})
	}
	waveOf := map[string]int{}
	for i, w := range waves {
		for _, id := range w {
			waveOf[id] = i
		}
	}

	for i := range def.Steps {
		s := &def.Steps[i]
		ds := DryRunStep{
			ID: s.ID, Kind: stepKind(s), Provider: s.Provider, Model: s.Model,
			Shell: s.Shell, Skills: s.Skills, Tools: s.Tools,
			MaxTurns: effectiveTurns(s), Wave: waveOf[s.ID],
		}

		// Would its model resolve? This is the check that catches a provider
		// naming an env var nobody set on this machine.
		//
		// "On this machine" is the whole subtlety. A step placed on a worker
		// resolves its provider THERE — `provider: devin` means the devin on
		// that machine's PATH, holding that machine's credential — so checking
		// it here would fail a workflow that is correct, which is worse than
		// not checking it.
		if ds.Kind == "prompt" {
			ds.RunsOn = s.RunsOn
			if e.servesLocally(s.RunsOn) {
				prov, err := e.resolveLLM(s, "")
				if err != nil {
					fail(DryProblem{Step: s.ID, Field: "provider", Message: err.Error(), Fatal: true})
				} else {
					ds.Model = prov.Label
					prov.Close()
				}
				// resolveLLM builds the adapter without touching the program:
				// a `cli` provider whose binary is absent resolves fine and
				// then fails at the first turn with an exec error wrapped in an
				// HTTP error, which reads like a network fault and is not.
				// Caught here, where it costs nothing, and named as what it is.
				if s.Provider != "" {
					if p, err := e.catalog.Providers.Require(s.Provider); err == nil {
						if c := checkProvider(p); !c.Ready {
							fail(DryProblem{Step: s.ID, Field: "provider", Message: c.Problem, Fatal: true})
						} else if c.Detail != "" {
							ds.Shell = c.Detail // where the program actually is
						}
					}
				}
			} else {
				ds.Workers = e.labelHolders(s)
				if ds.Workers == 0 {
					fail(DryProblem{
						Step: s.ID, Field: "runs-on",
						Message: fmt.Sprintf("no worker online holds the label %q — this step would wait. "+
							"Add a machine from the Workers page, or use one of: %s",
							s.RunsOn, strings.Join(e.LocalLabels(), ", ")),
					})
				}
				// A placed step's skills still have to exist HERE, because they
				// travel with the job. This is the check that catches the
				// difference between "the machine lacks a tool" and "the
				// platform cannot pack the step at all".
				if _, err := e.bundleSkills(s.Skills); err != nil {
					fail(DryProblem{Step: s.ID, Field: "skills", Message: err.Error(), Fatal: true})
				}
				for _, t := range s.Tools {
					if skills.IsPlatformTool(t) {
						fail(DryProblem{
							Step: s.ID, Field: "tools", Fatal: true,
							Message: fmt.Sprintf("%q only exists in the server process, so this step cannot run on %q", t, s.RunsOn),
						})
					}
				}
			}
		}

		// Would its shell exist? A `run:` step on a machine without one fails
		// at the moment it runs, which is the expensive moment.
		//
		// Only for a step that runs HERE. A step placed on a worker is checked
		// against the pool instead: whether THIS machine has powershell says
		// nothing about a step that will execute on a Windows box, and failing
		// the dry run on it would be a false alarm for the normal case.
		if ds.Kind == "run" {
			ds.RunsOn = s.RunsOn
			if e.servesLocally(s.RunsOn) {
				if sh, err := shellFor(s); err != nil {
					fail(DryProblem{Step: s.ID, Field: "shell", Message: err.Error(), Fatal: true})
				} else {
					ds.Shell = sh.Name
				}
			} else if ds.Workers = e.labelHolders(s); ds.Workers == 0 {
				// Not fatal: a machine can be added in a minute, and a workflow
				// written for a pool it will join is not wrong. But a run that
				// waits half an hour for a label nobody holds is the single
				// failure this check exists to pre-empt.
				fail(DryProblem{
					Step: s.ID, Field: "runs-on",
					Message: fmt.Sprintf("no worker online holds the label %q — this step would wait. "+
						"Add a machine from the Workers page, or use one of: %s",
						s.RunsOn, strings.Join(e.LocalLabels(), ", ")),
				})
			}
		}

		// Which variables does it read, and are they set HERE? A step whose
		// token is missing fails at the moment it calls something, which is
		// late and reads like the API's fault. Names only — a dry run has to be
		// safe to paste into an issue.
		if keys := workflow.EnvKeys(s.Env); len(keys) > 0 {
			ds.Env = keys
			if e.servesLocally(s.RunsOn) {
				var missing []string
				for _, k := range keys {
					if _, ok := os.LookupEnv(k); !ok {
						missing = append(missing, k)
					}
				}
				if len(missing) > 0 {
					fail(DryProblem{
						Step: s.ID, Field: "env", Fatal: true,
						Message: strings.Join(missing, ", ") + " is not set in this process's environment",
					})
				}
			}
			// A placed step reads them on the worker, so this machine's
			// environment says nothing about whether they are there.
		}

		if s.Classifier != "" {
			if _, err := e.catalog.Classifiers.Require(s.Classifier); err != nil {
				fail(DryProblem{Step: s.ID, Field: "classifier", Message: err.Error(), Fatal: true})
			}
		}

		// Would its template render? A hyphen in a step id breaks `.Steps.a-b`,
		// and that error only ever appeared mid-run.
		text := s.Prompt
		if text == "" {
			text = s.Run
		}
		if text != "" {
			data := dryTemplateData(def, sample, waveOf, s.ID)
			data.Step, data.Workflow = state[model.StateScopeStep], state[model.StateScopeWorkflow]
			data.Project, data.Global = state[model.StateScopeProject], state[model.StateScopeGlobal]
			// Rendered twice on purpose. The lenient render is what the agent
			// would ACTUALLY read, so it is what we show. The strict render is
			// the check: at run time a missing key silently becomes empty, so a
			// prompt referencing a field that does not exist just loses that
			// sentence and nobody ever finds out.
			rendered, err := workflow.Render(text, data)
			if err != nil {
				fail(DryProblem{Step: s.ID, Field: fieldName(s), Message: "template does not render: " + err.Error(), Fatal: true})
			} else {
				ds.Prompt = rendered
			}
			// What it REFERENCES, checked against what exists — see dryrefs.go
			// for why rendering alone catches almost nothing here.
			for _, p := range checkRefs(def, s, text, waveOf) {
				fail(p)
			}
			for _, p := range checkStateRefs(s, text, state) {
				fail(p)
			}
		}

		// Budgets: a step with no ceiling can run until the provider stops it.
		if ds.Kind == "prompt" && ds.MaxTurns == 0 {
			fail(DryProblem{Step: s.ID, Field: "budget", Fatal: false,
				Message: "no turn ceiling — the step is told it is unbounded and can spend until the provider stops it"})
		}
		if ds.Kind == "prompt" {
			out.Cost.AgentSteps++
			out.Cost.MaxTurns += ds.MaxTurns
			if s.Budget != nil {
				out.Cost.MaxToolCalls += s.Budget.MaxToolCalls
				out.Cost.MaxWallSec += s.Budget.MaxWallSec
			}
		} else {
			out.Cost.FreeSteps++
		}

		out.Steps = append(out.Steps, ds)
	}

	out.Problems = append(out.Problems, e.dryCatalog(def)...)
	out.Problems = append(out.Problems, e.dryRequirements(def)...)
	for _, p := range out.Problems {
		if p.Fatal {
			out.OK = false
		}
	}
	return out
}

// dryCatalog reports names that do not resolve on this machine. The loader
// already refuses an unknown name, so what is left is the subtler case: a name
// that exists but could not actually run — a provider whose key is unset, a
// CLI that is not installed.
// dryRequirements is the PRE-INSTALL CHECK: the configuration and the folders a
// workflow needs, asked of this machine before anything runs.
//
// It gathers with bundle.Requirements and asks with bundle.CheckRequirements —
// the same pair that refuses a pull the host cannot serve. One gather and one
// check, surfaced in three places: here before a run, at pull before a bundle is
// installed, and in `wfx-runner setup` on the machine being prepared. A second
// implementation of "is this machine ready" would drift from the first, and it
// would drift towards optimism, because the copy is the one nobody tests.
//
// Only `env` and `volume` are reported here. A provider, a label and an MCP
// server are already checked by dryCatalog and by the cost and placement passes,
// and reporting them twice would make the same problem look like two.
func (e *Engine) dryRequirements(def *workflow.Definition) []DryProblem {
	reqs := bundle.Requirements(def, e.catalog)
	if len(reqs) == 0 {
		return nil
	}
	var want []bundle.Requirement
	for _, r := range reqs {
		if r.Kind == bundle.ReqEnv || r.Kind == bundle.ReqVolume {
			want = append(want, r)
		}
	}
	if len(want) == 0 {
		return nil
	}
	var out []DryProblem
	// sourceHost answers a `./` volume against the workflow's own directory, which
	// only this side knows. A pull-time check has no definition and asks the plain
	// question; here the folder beside the workflow is exactly the thing to check.
	host := &dryHost{DoctorHost: e.RequirementHost(), sourceDir: def.SourceDir()}
	for _, u := range bundle.CheckRequirements(want, host).Unmet {
		step := ""
		if len(u.Requirement.Steps) > 0 {
			step = u.Requirement.Steps[0]
		}
		// Fatal: a missing variable or an unwritable folder does not degrade the
		// run, it fails it — and failing here costs nothing while failing later
		// costs a checkout, a worktree and the first tokens.
		out = append(out, DryProblem{
			Step:    step,
			Field:   u.Requirement.Kind,
			Fatal:   true,
			Message: u.Because + " — " + u.Fix,
		})
	}
	return out
}

// dryHost is the requirement host for a dry run: the machine's own answers, plus
// the workflow's directory so a `./` mount is checked where it actually lives.
type dryHost struct {
	*DoctorHost
	sourceDir string
}

func (d *dryHost) VolumeState(host string, writable bool) bundle.VolumeReadiness {
	return d.VolumeStateFrom(host, d.sourceDir, writable)
}

func (e *Engine) dryCatalog(def *workflow.Definition) []DryProblem {
	var out []DryProblem
	seen := map[string]bool{}
	for i := range def.Steps {
		s := &def.Steps[i]
		for _, name := range s.MCP {
			if seen["mcp:"+name] {
				continue
			}
			seen["mcp:"+name] = true
			if !e.catalog.Mcp.Has(name) {
				out = append(out, DryProblem{Step: s.ID, Field: "mcp", Fatal: true,
					Message: fmt.Sprintf("mcp server %q is not in the registry", name)})
			}
		}
		for _, name := range s.Skills {
			if seen["skill:"+name] {
				continue
			}
			seen["skill:"+name] = true
			if len(e.skills.RootsFor([]string{name})) == 0 {
				out = append(out, DryProblem{Step: s.ID, Field: "skills", Fatal: true,
					Message: fmt.Sprintf("skill %q is not in the registry on this machine", name)})
			}
		}
	}
	return out
}

// dryWaves computes the order steps would run in, by the same rules the engine
// uses — so the picture is the engine's opinion, not a second implementation
// that can disagree with it.
func (e *Engine) dryWaves(def *workflow.Definition, input map[string]any) ([][]string, string, error) {
	switch {
	case def.Goal != "":
		return dryPlanWaves(def, input)
	case anyNeeds(def):
		return dryDAGWaves(def)
	}
	var seq [][]string
	for i := range def.Steps {
		seq = append(seq, []string{def.Steps[i].ID})
	}
	return seq, "sequential", nil
}

// dryPlanWaves walks the planner optimistically: every step that runs is
// assumed to succeed and to establish what it produces.
//
// A guard is treated as HOLDING, because its value comes from a model that has
// not run. That is stated rather than hidden: the waves for a goal-planned
// workflow are the widest possible route, not a prediction.
func dryPlanWaves(def *workflow.Definition, input map[string]any) ([][]string, string, error) {
	plan := buildPlan(def, input)
	if err := plan.Validate(); err != nil {
		return nil, "goal-planned", err
	}
	// Guards are dropped for the walk: with no real outputs, every `when` would
	// read false and the plan would look stuck when it is not.
	for i := range plan.Actions {
		plan.Actions[i].Guard = nil
	}
	world := planner.NewWorld()
	for k, v := range input {
		world.Assert(k, v)
	}
	done := map[string]bool{}
	var waves [][]string
	for range def.Steps {
		ready := plan.Ready(world, done)
		if len(ready) == 0 {
			break
		}
		var wave []string
		for _, a := range ready {
			wave = append(wave, a.ID)
			done[a.ID] = true
		}
		sort.Strings(wave)
		waves = append(waves, wave)
		for _, a := range ready {
			for _, f := range a.Produces {
				world.Assert(f, "(dry run)")
			}
		}
		if plan.Done(world, done) {
			break
		}
	}
	if !plan.Done(world, done) {
		return waves, "goal-planned", fmt.Errorf("the goal %q cannot be reached even if every step succeeds: %w",
			def.Goal, plan.Stuck(world, done))
	}
	return waves, "goal-planned", nil
}

// dryDAGWaves groups steps into the waves `needs` implies.
func dryDAGWaves(def *workflow.Definition) ([][]string, string, error) {
	done := map[string]bool{}
	var waves [][]string
	for len(done) < len(def.Steps) {
		var wave []string
		for i := range def.Steps {
			s := &def.Steps[i]
			if done[s.ID] {
				continue
			}
			ready := true
			for _, n := range s.Needs {
				if !done[n] {
					ready = false
					break
				}
			}
			if ready {
				wave = append(wave, s.ID)
			}
		}
		if len(wave) == 0 {
			return waves, "parallel", fmt.Errorf("steps cannot start: a `needs` cycle or an unknown dependency")
		}
		sort.Strings(wave)
		for _, id := range wave {
			done[id] = true
		}
		waves = append(waves, wave)
	}
	return waves, "parallel", nil
}

func anyNeeds(def *workflow.Definition) bool {
	for i := range def.Steps {
		if len(def.Steps[i].Needs) > 0 {
			return true
		}
	}
	return false
}

func stepKind(s *workflow.Step) string {
	switch {
	case s.Run != "":
		return "run"
	case s.Judge != nil:
		return "judge"
	}
	return "prompt"
}

// dryTemplateData gives a template everything it could legitimately reference:
// the sample input, and a placeholder output for every step in an EARLIER wave.
//
// Only earlier waves, deliberately. A template referencing a step that runs
// after it is a real bug — it would render empty at run time and the author
// would never know — so the dry run must not paper over it by offering every
// step's output.
func dryTemplateData(def *workflow.Definition, input map[string]any, waveOf map[string]int, stepID string) workflow.TemplateData {
	steps := map[string]any{}
	mine := waveOf[stepID]
	for i := range def.Steps {
		s := &def.Steps[i]
		if s.ID == stepID || waveOf[s.ID] >= mine {
			continue
		}
		steps[s.ID] = sampleOutput(s)
	}
	return workflow.TemplateData{
		RunID:   "dry-run",
		WorkDir: "/dry-run/workspace",
		BaseRef: "0000000",
		Input:   input,
		Steps:   steps,
		Output:  map[string]any{},
		Decide:  map[string]any{},
	}
}

// dryState is what each scope would hold for THIS workflow: whatever is stored
// now, plus every key the workflow itself declares it will write. A reference
// to anything else is a key nobody produces — usually a typo.
//
// The step scope is flattened into one map on purpose: the check is "does any
// step write this key", not "which one", and telling an author their key is
// written by a different step of the same workflow would be noise.
func (e *Engine) dryState(def *workflow.Definition) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, scope := range model.StateScopes {
		out[scope] = map[string]string{}
	}
	if e.store != nil {
		ctx := context.Background()
		for _, scope := range model.StateScopes {
			name, err := stateNameFor(scope, def.Name, "", "")
			if scope == model.StateScopeStep {
				// Every step of this workflow, whatever it is called.
				if vars, err := e.store.ListStateByPrefix(ctx, scope, def.Name+"/"); err == nil {
					for _, v := range vars {
						out[scope][v.Key] = v.Value
					}
				}
				continue
			}
			if err != nil {
				continue // project state has no address without a run
			}
			if m, err := e.store.StateFor(ctx, scope, name); err == nil {
				out[scope] = m
			}
		}
	}
	for i := range def.Steps {
		for scope, entries := range def.Steps[i].State {
			if out[scope] == nil {
				continue
			}
			for key := range entries {
				if _, ok := out[scope][key]; !ok {
					out[scope][key] = ""
				}
			}
		}
	}
	return out
}

// sampleOutput builds a plausible object for a step's declared output schema,
// so a template referencing a real field renders and one referencing a field
// the schema does not declare renders as "<no value>" — which is the bug.
func sampleOutput(s *workflow.Step) map[string]any {
	out := map[string]any{}
	props, _ := s.OutputSchema["properties"].(map[string]any)
	for name, raw := range props {
		p, _ := raw.(map[string]any)
		out[name] = sampleValue(p)
	}
	return out
}

func sampleValue(p map[string]any) any {
	if p == nil {
		return "(dry run)"
	}
	if def, ok := p["default"]; ok {
		return def
	}
	if enum, ok := p["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	switch p["type"] {
	case "boolean":
		return true
	case "integer", "number":
		return 1
	case "array":
		item, _ := p["items"].(map[string]any)
		return []any{sampleValue(item)}
	case "object":
		inner, _ := p["properties"].(map[string]any)
		out := map[string]any{}
		for k, v := range inner {
			vp, _ := v.(map[string]any)
			out[k] = sampleValue(vp)
		}
		return out
	}
	return "(dry run)"
}

// sampleInput fills what a real run would collect from a form.
func sampleInput(def *workflow.Definition, given map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range given {
		out[k] = v
	}
	props, _ := def.InputSchema["properties"].(map[string]any)
	for name, raw := range props {
		if _, ok := out[name]; ok {
			continue
		}
		p, _ := raw.(map[string]any)
		if d, ok := p["default"]; ok {
			out[name] = d
			continue
		}
		out[name] = sampleValue(p)
	}
	// A workflow acting on a repository needs somewhere to act; the working
	// directory is a placeholder, and nothing is read from it.
	if _, ok := out["repo_path"]; !ok {
		if wd, err := os.Getwd(); err == nil {
			out["repo_path"] = wd
		}
	}
	return out
}

// Summary is the one-paragraph verdict, for a CLI and for an agent reading its
// own tool result.
func (d *DryRun) Summary() string {
	var b strings.Builder
	verdict := "would run"
	if !d.OK {
		verdict = "WOULD FAIL"
	}
	fmt.Fprintf(&b, "%s: %s — %s, %d steps in %d wave(s)",
		d.Workflow, verdict, d.Shape, len(d.Steps), len(d.Waves))
	if d.Cost.AgentSteps > 0 {
		fmt.Fprintf(&b, "; at most %d model turns across %d agent step(s)", d.Cost.MaxTurns, d.Cost.AgentSteps)
	}
	if d.Cost.FreeSteps > 0 {
		fmt.Fprintf(&b, "; %d step(s) call no model", d.Cost.FreeSteps)
	}
	if len(d.Problems) == 0 {
		b.WriteString(". No problems found.")
		return b.String()
	}
	b.WriteString(".\n")
	for _, p := range d.Problems {
		mark := "warning"
		if p.Fatal {
			mark = "FATAL"
		}
		where := p.Step
		if where != "" && p.Field != "" {
			where += "." + p.Field
		}
		fmt.Fprintf(&b, "  %s %s: %s\n", mark, where, p.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

// DryRunDraft validates a definition the way the loader would and then dry-runs
// it. A draft has not been through the loader, so the validation that a saved
// workflow already passed has to happen here — otherwise the dry run reports on
// a shape the platform would refuse to load at all.
func (e *Engine) DryRunDraft(def *workflow.Definition, input map[string]any) *DryRun {
	if err := workflow.Check(def, e.validator()); err != nil {
		return &DryRun{
			Workflow: def.Name,
			OK:       false,
			Problems: []DryProblem{{Message: err.Error(), Fatal: true}},
		}
	}
	return e.DryRunDefinition(def, input)
}

// labelHolders is how many workers currently serve a step's label. It answers
// the question a dry run exists for — would this step wait? — without costing
// anything.
func (e *Engine) labelHolders(s *workflow.Step) int {
	if e.store == nil {
		return 0
	}
	online, err := e.store.OnlineLabels(context.Background())
	if err != nil {
		return 0
	}
	return online[s.RunsOn]
}
