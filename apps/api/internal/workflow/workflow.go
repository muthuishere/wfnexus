// Package workflow loads declarative workflow definitions (YAML) and renders step prompts.
//
// A workflow is an ordered list of steps. Each step is ONE toolnexus agent run:
// a prompt, a scoped set of agent skills + tools, and a JSON-schema output
// contract the agent must satisfy by calling `submit_output`.
package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

type Gate struct {
	// Field is a top-level key of the step output; when it equals Equals the Action fires.
	Field  string `yaml:"field" json:"field"`
	Equals any    `yaml:"equals" json:"equals"`
	// Action: "needs_input" (pause, ask the human for more), "fail" (stop the run), "skip_to" (jump to step SkipTo).
	Action string `yaml:"action" json:"action"`
	SkipTo string `yaml:"skip_to,omitempty" json:"skipTo,omitempty"`
	// Message is shown to the human; may reference {{ .Output.<field> }}.
	Message string `yaml:"message,omitempty" json:"message,omitempty"`
}

// Budget caps one step's agent subtree. Every limit stop is loud.
type Budget struct {
	MaxTurns      int   `yaml:"max_turns,omitempty" json:"maxTurns,omitempty"`
	MaxTokens     int64 `yaml:"max_tokens,omitempty" json:"maxTokens,omitempty"`
	MaxToolCalls  int64 `yaml:"max_tool_calls,omitempty" json:"maxToolCalls,omitempty"`
	MaxWallSec    int   `yaml:"max_wall_sec,omitempty" json:"maxWallSec,omitempty"`
	MaxChildren   int   `yaml:"max_children,omitempty" json:"maxChildren,omitempty"`
	MaxConcurrent int   `yaml:"max_concurrent,omitempty" json:"maxConcurrent,omitempty"`
	MaxDepth      int   `yaml:"max_depth,omitempty" json:"maxDepth,omitempty"`
}

// Guardrail is a POLICY check on a tool call — "may it?", never "is it right?".
// First deny wins; the model sees the denial as the tool result and reacts.
type Guardrail struct {
	// Deny names the tool this rule governs; "*" matches any tool.
	Deny string `yaml:"deny" json:"deny"`
	// ArgsContain denies only when any argument's text contains one of these.
	// Empty ⇒ the tool is denied outright.
	ArgsContain []string `yaml:"args_contain,omitempty" json:"argsContain,omitempty"`
	Reason      string   `yaml:"reason" json:"reason"`
}

// TeamMember is a sub-agent this step may delegate to via the built-in `task`
// tool. Delegation is opt-in: no team ⇒ no task tool.
type TeamMember struct {
	ID string `yaml:"id" json:"id"`
	// Does is the routing description the delegating model reads.
	Does   string   `yaml:"does" json:"does"`
	Soul   string   `yaml:"soul,omitempty" json:"soul,omitempty"`
	Skills []string `yaml:"skills,omitempty" json:"skills,omitempty"`
	Tools  []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	Model  string   `yaml:"model,omitempty" json:"model,omitempty"`
	Budget *Budget  `yaml:"budget,omitempty" json:"budget,omitempty"`
}

// Question is one typed classifier question asked before the step's agent runs.
// Cheap, fast, calibrated — a judgment that steers, never an action.
type Question struct {
	// Type is "noul" (0..1 truth), "choice" (one of your options) or "score"
	// (a level on an ordered rubric).
	Type         string `yaml:"type" json:"type"`
	Instructions string `yaml:"instructions" json:"instructions"`
	// Options maps option id -> what PICKING IT WOULD MEAN (choice only).
	// Describing options by consequence is what makes the answer meaningful.
	Options map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
	// Levels is the ordered rubric, 2-10 entries (score only). Level 0 is first.
	Levels []string `yaml:"levels,omitempty" json:"levels,omitempty"`
	// True/False describe the two cases (noul only, optional).
	True  string `yaml:"true,omitempty" json:"true,omitempty"`
	False string `yaml:"false,omitempty" json:"false,omitempty"`
}

// DecideGate branches on a classifier answer before the expensive agent runs.
type DecideGate struct {
	Question string `yaml:"question" json:"question"`
	// Exactly one of Below / AtLeast / Is applies.
	Below   *float64 `yaml:"below,omitempty" json:"below,omitempty"`
	AtLeast *float64 `yaml:"at_least,omitempty" json:"atLeast,omitempty"`
	Is      string   `yaml:"is,omitempty" json:"is,omitempty"`
	Action  string   `yaml:"action" json:"action"`
	SkipTo  string   `yaml:"skip_to,omitempty" json:"skipTo,omitempty"`
	Message string   `yaml:"message,omitempty" json:"message,omitempty"`
}

// Guard is a condition over the facts produced so far. Path is dotted
// (`triage.valid`); exactly one of Equals / Exists applies.
type Guard struct {
	Path   string `yaml:"path" json:"path"`
	Equals any    `yaml:"equals,omitempty" json:"equals,omitempty"`
	Exists *bool  `yaml:"exists,omitempty" json:"exists,omitempty"`
}

// Retry re-runs a whole step that failed. It is distinct from the completion
// gate's max_attempts, which retries the MODEL inside one step: this retries
// the step itself, including its tools and its workspace side effects — so a
// step with a retry policy must be idempotent in effect.
type Retry struct {
	MaxAttempts int `yaml:"max_attempts" json:"maxAttempts"`
	BackoffSec  int `yaml:"backoff_sec,omitempty" json:"backoffSec,omitempty"`
}

// Decide is the step's classifier pass: typed questions answered by a small
// model before the agent starts, with gates that can route or halt cheaply.
type Decide struct {
	// State is the text handed to the classifier (templated like a prompt).
	State     string              `yaml:"state" json:"state"`
	Questions map[string]Question `yaml:"questions" json:"questions"`
	Gates     []DecideGate        `yaml:"gates,omitempty" json:"gates,omitempty"`
}

type Step struct {
	// JobID is the job this step was flattened out of, when the workflow used
	// the jobs form. Empty for a flat step list.
	JobID string `yaml:"-" json:"jobId,omitempty"`

	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Prompt      string `yaml:"prompt" json:"prompt"`
	System      string `yaml:"system,omitempty" json:"system,omitempty"`
	// Skills is the per-step allowlist of agent skills (SKILL.md names).
	Skills []string `yaml:"skills" json:"skills"`
	// Tools is the per-step allowlist of toolnexus built-in tools (bash, read, write, edit, grep, glob, apply_patch, webfetch, todowrite, question).
	Tools []string `yaml:"tools" json:"tools"`
	// MCP lists mcp.json server names exposed to this step (empty ⇒ none).
	MCP          []string       `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	OutputSchema map[string]any `yaml:"output_schema" json:"outputSchema"`
	Model        string         `yaml:"model,omitempty" json:"model,omitempty"`
	MaxTurns     int            `yaml:"max_turns,omitempty" json:"maxTurns,omitempty"`
	MaxAttempts  int            `yaml:"max_attempts,omitempty" json:"maxAttempts,omitempty"`
	TimeoutSec   int            `yaml:"timeout_sec,omitempty" json:"timeoutSec,omitempty"`
	// RequiresApproval pauses the run BEFORE this step until a human approves.
	RequiresApproval bool   `yaml:"requires_approval,omitempty" json:"requiresApproval,omitempty"`
	Gates            []Gate `yaml:"gates,omitempty" json:"gates,omitempty"`

	// Run makes this step a DETERMINISTIC NODE rather than an agent: the shell
	// command is executed in the run's workspace, no model is called, and it
	// costs nothing. Exactly GitHub Actions' `run:`. A step has `run` or
	// `prompt`, never both.
	//
	// Its output is fixed — {ok, exitCode, stdout, stderr} — so a `when` guard
	// or a gate can branch on `tests.ok` the same way it branches on an agent's
	// output. Use it for the parts of a workflow that do not need judgment:
	// running a suite, a linter, a git operation.
	Run string `yaml:"run,omitempty" json:"run,omitempty"`
	// Soul is the agent's identity for this step (its system prompt).
	Soul string `yaml:"soul,omitempty" json:"soul,omitempty"`
	// Budget caps this step's agent subtree.
	Budget *Budget `yaml:"budget,omitempty" json:"budget,omitempty"`
	// Guardrails are policy checks on this step's tool calls.
	Guardrails []Guardrail `yaml:"guardrails,omitempty" json:"guardrails,omitempty"`
	// Team are sub-agents this step may delegate to.
	Team []TeamMember `yaml:"team,omitempty" json:"team,omitempty"`
	// Decide is the classifier pass that runs before the agent.
	Decide *Decide `yaml:"decide,omitempty" json:"decide,omitempty"`
	// Consumes are the FACTS this step needs before it can run; Produces are
	// the facts it establishes. Declaring these instead of `needs` lets the
	// planner derive the order and re-derive it after every step, so the plan
	// responds to what the agents actually found (see internal/planner).
	Consumes []string `yaml:"consumes,omitempty" json:"consumes,omitempty"`
	Produces []string `yaml:"produces,omitempty" json:"produces,omitempty"`
	// When is an extra guard on applicability, evaluated against the values
	// produced so far — the same field/equals shape as a gate.
	When []Guard `yaml:"when,omitempty" json:"when,omitempty"`
	// Needs lists the steps that must finish before this one may start. Declaring
	// it anywhere turns the workflow into a DAG: every step whose dependencies
	// are satisfied runs CONCURRENTLY. Empty everywhere ⇒ strictly sequential,
	// exactly as before.
	Needs []string `yaml:"needs,omitempty" json:"needs,omitempty"`
	// Provider names an entry in the provider registry; empty ⇒ the default.
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	// Classifier names an entry in the classifier registry, for `decide`.
	Classifier string `yaml:"classifier,omitempty" json:"classifier,omitempty"`
	// Retry re-runs the whole step on failure.
	Retry *Retry `yaml:"retry,omitempty" json:"retry,omitempty"`
	// AskHuman grants the `question` built-in and makes a suspension durable:
	// the run parks in needs_input until a human answers.
	AskHuman bool `yaml:"ask_human,omitempty" json:"askHuman,omitempty"`
}

type Definition struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description"`
	InputSchema map[string]any `yaml:"input_schema" json:"inputSchema"`
	Steps       []Step         `yaml:"steps" json:"steps"`
	// Jobs are the GitHub-Actions-shaped form: jobs run in parallel, `needs`
	// orders them, and each holds an ordered list of steps. Flattened into
	// Steps at load time (see jobs.go), so nothing downstream knows about them.
	Jobs map[string]*Job `yaml:"jobs,omitempty" json:"jobs,omitempty"`
	// Uses expand reusable TASKS into steps at load time (see task.go).
	Uses []Use `yaml:"uses,omitempty" json:"uses,omitempty"`
	// Template marks a workflow as a starting point to copy rather than run.
	Template bool `yaml:"template,omitempty" json:"template,omitempty"`
	// authoredNeeds records whether the AUTHOR wrote any dependency, as opposed
	// to the synthetic edges job flattening creates to chain a job's own steps.
	// Only authored edges conflict with a derived plan; a job's internal
	// chaining is an implementation detail of the job.
	authoredNeeds bool
	// Goal is the fact the workflow must establish. Declaring it turns execution
	// over to the planner: the order is derived from what each step consumes and
	// produces, and re-derived after every step.
	Goal string `yaml:"goal,omitempty" json:"goal,omitempty"`
	// MaxParallel caps how many of this workflow's steps run at once when it is
	// a DAG or a plan. 0 ⇒ 4.
	MaxParallel int    `yaml:"max_parallel,omitempty" json:"maxParallel,omitempty"`
	Path        string `yaml:"-" json:"path"`
}

func (d *Definition) Step(id string) (int, *Step) {
	for i := range d.Steps {
		if d.Steps[i].ID == id {
			return i, &d.Steps[i]
		}
	}
	return -1, nil
}

// Catalog is what a workflow is validated against: the skill registry and the
// built-in tool set. An interface, so the loader stays testable without one.
type Catalog interface {
	Missing(names []string) []string         // skills not in the registry
	MissingBuiltins(names []string) []string // tool names that are not built-ins
	MissingProviders(names []string) []string
	MissingClassifiers(names []string) []string
	MissingMcp(names []string) []string
}

func (d *Definition) validate(cat Catalog) error {
	if d.Name == "" {
		return fmt.Errorf("workflow name is required")
	}
	seen := map[string]bool{}
	for i, s := range d.Steps {
		if s.ID == "" {
			return fmt.Errorf("%s: step %d needs an id", d.Name, i)
		}
		if s.Prompt == "" && s.Run == "" {
			return fmt.Errorf("%s/%s: a step needs either `prompt` (an agent) or `run` (a command)", d.Name, s.ID)
		}
		if s.Prompt != "" && s.Run != "" {
			return fmt.Errorf("%s/%s: a step is an agent OR a command, not both — `prompt` and `run` are mutually exclusive", d.Name, s.ID)
		}
		if s.Run != "" {
			if len(s.Skills) > 0 || len(s.Team) > 0 || s.Decide != nil {
				return fmt.Errorf("%s/%s: a `run` step calls no model, so skills, team and decide have no meaning on it", d.Name, s.ID)
			}
		}
		if seen[s.ID] {
			return fmt.Errorf("%s: duplicate step id %q", d.Name, s.ID)
		}
		seen[s.ID] = true
		if s.OutputSchema == nil && s.Run == "" {
			return fmt.Errorf("%s: step %q needs output_schema", d.Name, s.ID)
		}
		if cat != nil {
			if miss := cat.Missing(s.Skills); len(miss) > 0 {
				return fmt.Errorf("%s/%s: skills not in the registry: %s", d.Name, s.ID, strings.Join(miss, ", "))
			}
			if miss := cat.MissingBuiltins(s.Tools); len(miss) > 0 {
				return fmt.Errorf("%s/%s: not built-in tools: %s", d.Name, s.ID, strings.Join(miss, ", "))
			}
			if s.Provider != "" {
				if miss := cat.MissingProviders([]string{s.Provider}); len(miss) > 0 {
					return fmt.Errorf("%s/%s: unknown provider %q", d.Name, s.ID, s.Provider)
				}
			}
			if s.Classifier != "" {
				if miss := cat.MissingClassifiers([]string{s.Classifier}); len(miss) > 0 {
					return fmt.Errorf("%s/%s: unknown classifier %q", d.Name, s.ID, s.Classifier)
				}
			}
			if miss := cat.MissingMcp(s.MCP); len(miss) > 0 {
				return fmt.Errorf("%s/%s: mcp servers not in the registry: %s", d.Name, s.ID, strings.Join(miss, ", "))
			}
		}
		if err := s.validateTeam(d.Name, cat); err != nil {
			return err
		}
		if err := s.validateGuardrails(d.Name, cat); err != nil {
			return err
		}
		if err := s.validateDecide(d.Name); err != nil {
			return err
		}
		if s.Retry != nil && s.Retry.MaxAttempts < 1 {
			return fmt.Errorf("%s/%s: retry.max_attempts must be >= 1 — an unbounded retry is a runaway", d.Name, s.ID)
		}
		for _, g := range s.Gates {
			switch g.Action {
			case "needs_input", "fail":
			case "skip_to":
				if g.SkipTo == "" {
					return fmt.Errorf("%s/%s: skip_to gate needs skip_to", d.Name, s.ID)
				}
			default:
				return fmt.Errorf("%s/%s: unknown gate action %q", d.Name, s.ID, g.Action)
			}
			if g.Field == "" {
				return fmt.Errorf("%s/%s: gate needs a field", d.Name, s.ID)
			}
		}
	}
	// skip_to targets are resolved after every id is known, so forward jumps work
	for _, s := range d.Steps {
		for _, g := range s.Gates {
			if g.Action == "skip_to" && !seen[g.SkipTo] {
				return fmt.Errorf("%s/%s: skip_to unknown step %q", d.Name, s.ID, g.SkipTo)
			}
		}
	}
	if err := d.validateGraph(seen); err != nil {
		return err
	}
	return d.validatePlan()
}

// validatePlan refuses a plan that cannot work before anything runs: a fact
// nothing produces, a goal nothing establishes, a guard with no condition, and
// mixing derived order with hand-written edges.
func (d *Definition) validatePlan() error {
	if !d.IsPlanned() {
		return nil
	}
	if d.authoredNeeds {
		return fmt.Errorf("%s: a workflow declares its order EITHER with `needs` OR with consumes/produces and a goal — not both, because the two would disagree the moment a guard fails", d.Name)
	}
	for _, s := range d.Steps {
		for _, g := range s.When {
			if g.Path == "" {
				return fmt.Errorf("%s/%s: a `when` guard needs a path", d.Name, s.ID)
			}
			if g.Equals == nil && g.Exists == nil {
				return fmt.Errorf("%s/%s: `when` guard on %q needs equals or exists", d.Name, s.ID, g.Path)
			}
		}
	}
	return nil
}

// IsPlanned reports whether this workflow's order is DERIVED rather than
// written down: a goal, or any step declaring facts.
func (d *Definition) IsPlanned() bool {
	if d.Goal != "" {
		return true
	}
	for _, s := range d.Steps {
		if len(s.Consumes) > 0 || len(s.Produces) > 0 {
			return true
		}
	}
	return false
}

// IsDAG reports whether any step declares dependencies. A workflow that
// declares none runs strictly sequentially, byte-identically to before.
func (d *Definition) IsDAG() bool {
	for _, s := range d.Steps {
		if len(s.Needs) > 0 {
			return true
		}
	}
	return false
}

// validateGraph checks the dependency graph: known ids, no self-edge, no cycle,
// and no skip_to — a forward jump is a sequential idea with no meaning once
// steps run concurrently.
func (d *Definition) validateGraph(known map[string]bool) error {
	if !d.IsDAG() {
		return nil
	}
	for _, s := range d.Steps {
		for _, dep := range s.Needs {
			if dep == s.ID {
				return fmt.Errorf("%s/%s: a step cannot need itself", d.Name, s.ID)
			}
			if !known[dep] {
				return fmt.Errorf("%s/%s: needs unknown step %q", d.Name, s.ID, dep)
			}
		}
		for _, g := range s.Gates {
			if g.Action == "skip_to" {
				return fmt.Errorf("%s/%s: skip_to cannot be used in a parallel workflow — "+
					"a forward jump has no meaning once steps run concurrently; express the "+
					"condition with `needs` and a fail/needs_input gate instead", d.Name, s.ID)
			}
		}
		if s.Decide != nil {
			for _, g := range s.Decide.Gates {
				if g.Action == "skip_to" {
					return fmt.Errorf("%s/%s: decide skip_to cannot be used in a parallel workflow", d.Name, s.ID)
				}
			}
		}
	}
	return d.detectCycle()
}

// detectCycle reports the first cycle it finds, naming the path, because "there
// is a cycle" is not actionable and "a → b → a" is.
func (d *Definition) detectCycle() error {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}
	deps := map[string][]string{}
	for _, s := range d.Steps {
		deps[s.ID] = s.Needs
	}
	var path []string
	var visit func(id string) error
	visit = func(id string) error {
		switch colour[id] {
		case grey:
			return fmt.Errorf("%s: dependency cycle: %s", d.Name, strings.Join(append(path, id), " → "))
		case black:
			return nil
		}
		colour[id] = grey
		path = append(path, id)
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		colour[id] = black
		return nil
	}
	for _, s := range d.Steps {
		if err := visit(s.ID); err != nil {
			return err
		}
	}
	return nil
}

// Ready returns the steps whose dependencies are all satisfied and which have
// not run yet — the next wave to execute concurrently.
func (d *Definition) Ready(done map[string]bool, started map[string]bool) []*Step {
	var out []*Step
	for i := range d.Steps {
		s := &d.Steps[i]
		if done[s.ID] || started[s.ID] {
			continue
		}
		ok := true
		for _, dep := range s.Needs {
			if !done[dep] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// LoadDir reads every *.yaml / *.yml in dir.
func LoadDir(dir string, cat Catalog) (map[string]*Definition, error) {
	return LoadDirWithTasks(dir, "", cat)
}

// LoadDirWithTasks loads workflows, expanding any reusable tasks found in
// tasksDir before validation — so a task's steps are checked exactly like
// hand-written ones.
func LoadDirWithTasks(dir, tasksDir string, cat Catalog) (map[string]*Definition, error) {
	tasks, err := LoadTasks(tasksDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]*Definition{}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		d := &Definition{}
		if err := yaml.Unmarshal(raw, d); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		d.Path = p
		for _, st := range d.Steps {
			if len(st.Needs) > 0 {
				d.authoredNeeds = true
			}
		}
		if err := d.expandUses(tasks); err != nil {
			return nil, err
		}
		if err := d.expandJobs(tasks); err != nil {
			return nil, err
		}
		normalize(d)
		if err := d.validate(cat); err != nil {
			return nil, err
		}
		out[d.Name] = d
	}
	return out, nil
}

// normalize converts yaml's map[string]any (already string-keyed in yaml.v3) and
// fills defaults.
func normalize(d *Definition) {
	if d.InputSchema == nil {
		d.InputSchema = map[string]any{"type": "object"}
	}
	if d.MaxParallel <= 0 {
		d.MaxParallel = 4
	}
	for i := range d.Steps {
		s := &d.Steps[i]
		if s.Name == "" {
			s.Name = s.ID
		}
		if s.MaxTurns == 0 {
			s.MaxTurns = 30
		}
		if s.MaxAttempts == 0 {
			s.MaxAttempts = 3
		}
		if s.Retry != nil && s.Retry.MaxAttempts == 0 {
			s.Retry.MaxAttempts = 1
		}
		if s.Run != "" && s.OutputSchema == nil {
			s.OutputSchema = RunOutputSchema()
		}
	}
}

// validateTeam checks each sub-agent is complete and scoped to real capabilities.
func (s *Step) validateTeam(wf string, cat Catalog) error {
	seen := map[string]bool{}
	for _, m := range s.Team {
		if m.ID == "" || m.Does == "" {
			return fmt.Errorf("%s/%s: every team member needs an id and a `does` (the routing description the model reads)", wf, s.ID)
		}
		if seen[m.ID] {
			return fmt.Errorf("%s/%s: duplicate team member %q", wf, s.ID, m.ID)
		}
		seen[m.ID] = true
		if m.ID == s.ID {
			return fmt.Errorf("%s/%s: a team member may not share the step's id", wf, s.ID)
		}
		if cat == nil {
			continue
		}
		if miss := cat.Missing(m.Skills); len(miss) > 0 {
			return fmt.Errorf("%s/%s/%s: skills not in the registry: %s", wf, s.ID, m.ID, strings.Join(miss, ", "))
		}
		if miss := cat.MissingBuiltins(m.Tools); len(miss) > 0 {
			return fmt.Errorf("%s/%s/%s: not built-in tools: %s", wf, s.ID, m.ID, strings.Join(miss, ", "))
		}
	}
	return nil
}

// validateGuardrails checks each policy rule names a real tool and a reason —
// a denial with no reason is invisible to the model that receives it.
func (s *Step) validateGuardrails(wf string, cat Catalog) error {
	for _, g := range s.Guardrails {
		if g.Deny == "" {
			return fmt.Errorf("%s/%s: a guardrail needs `deny` (a tool name, or * for any)", wf, s.ID)
		}
		if g.Reason == "" {
			return fmt.Errorf("%s/%s: guardrail on %q needs a reason — the model is shown it as the tool result", wf, s.ID, g.Deny)
		}
		if g.Deny != "*" && cat != nil {
			// a guardrail may name an MCP or skill tool, so only flag a name that
			// looks like a built-in typo: unknown AND the step grants built-ins
			if miss := cat.MissingBuiltins([]string{g.Deny}); len(miss) > 0 && len(s.Tools) > 0 && !toolGranted(s.Tools, g.Deny) {
				continue // not a built-in and not granted here — could be an MCP tool; allow it
			}
		}
	}
	return nil
}

func toolGranted(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

// validateDecide enforces the classifier's client-side limits and the encoding
// obligation: an option described only by its own id ranks at chance.
func (s *Step) validateDecide(wf string) error {
	if s.Decide == nil {
		return nil
	}
	if len(s.Decide.Questions) == 0 {
		return fmt.Errorf("%s/%s: decide needs at least one question", wf, s.ID)
	}
	for key, q := range s.Decide.Questions {
		if q.Instructions == "" {
			return fmt.Errorf("%s/%s: decide question %q needs instructions", wf, s.ID, key)
		}
		switch q.Type {
		case "noul":
		case "choice":
			if len(q.Options) < 1 || len(q.Options) > 255 {
				return fmt.Errorf("%s/%s: decide question %q needs 1..255 options, got %d", wf, s.ID, key, len(q.Options))
			}
			if degenerate(q.Options) {
				return fmt.Errorf("%s/%s: decide question %q describes its options degenerately (empty, or the id repeated) — "+
					"an option must say what PICKING IT would mean, or the answer ranks at chance", wf, s.ID, key)
			}
		case "score":
			if len(q.Levels) < 2 || len(q.Levels) > 10 {
				return fmt.Errorf("%s/%s: decide question %q needs an ordered rubric of 2..10 levels, got %d", wf, s.ID, key, len(q.Levels))
			}
		default:
			return fmt.Errorf("%s/%s: decide question %q has unknown type %q (noul|choice|score)", wf, s.ID, key, q.Type)
		}
	}
	for _, g := range s.Decide.Gates {
		if _, ok := s.Decide.Questions[g.Question]; !ok {
			return fmt.Errorf("%s/%s: decide gate references unknown question %q", wf, s.ID, g.Question)
		}
		n := 0
		for _, set := range []bool{g.Below != nil, g.AtLeast != nil, g.Is != ""} {
			if set {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("%s/%s: decide gate on %q needs exactly one of below / at_least / is", wf, s.ID, g.Question)
		}
		switch g.Action {
		case "needs_input", "fail":
		case "skip_to":
			if g.SkipTo == "" {
				return fmt.Errorf("%s/%s: decide skip_to gate needs skip_to", wf, s.ID)
			}
		default:
			return fmt.Errorf("%s/%s: decide gate has unknown action %q", wf, s.ID, g.Action)
		}
	}
	return nil
}

// degenerate reports the criteria shapes toolnexus warns about: every value
// empty, every value equal to its own key, or every value identical.
func degenerate(criteria map[string]string) bool {
	if len(criteria) < 2 {
		return false
	}
	allEmpty, allSelf, first, allSame := true, true, "", true
	for k, v := range criteria {
		if strings.TrimSpace(v) != "" {
			allEmpty = false
		}
		if v != k {
			allSelf = false
		}
		if first == "" {
			first = v
		} else if v != first {
			allSame = false
		}
	}
	return allEmpty || allSelf || allSame
}

// RunOutputSchema is the fixed contract of a `run` node, so a guard can branch
// on `build.ok` exactly as it would on an agent's output.
func RunOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"ok", "exitCode"},
		"properties": map[string]any{
			"ok":       map[string]any{"type": "boolean", "description": "exit code was zero"},
			"exitCode": map[string]any{"type": "integer"},
			"stdout":   map[string]any{"type": "string"},
			"stderr":   map[string]any{"type": "string"},
		},
	}
}

func Sorted(m map[string]*Definition) []*Definition {
	out := make([]*Definition, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TemplateData is what a step prompt can reference.
type TemplateData struct {
	RunID   string
	WorkDir string         // repo checkout dir for this run
	BaseRef string         // commit the run started from, for diffs
	Input   map[string]any // run input
	Steps   map[string]any // previous step outputs, keyed by step id
	Output  map[string]any // current step output (gate messages only)
	Decide  map[string]any // this step's judge answers, keyed by question
}

var funcs = template.FuncMap{
	"json": func(v any) string {
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	},
	// join accepts whatever a JSON decode produced ([]any of strings, numbers,
	// objects) as well as []string — a step output always arrives as []any.
	"join": func(v any, sep string) string { return strings.Join(toStrings(v), sep) },
	// list renders a markdown bullet list, the usual way a prompt wants an array.
	"list": func(v any) string {
		items := toStrings(v)
		if len(items) == 0 {
			return "(none)"
		}
		return "- " + strings.Join(items, "\n- ")
	},
	// stepval walks a path through decoded JSON without exploding: a step that
	// was skipped, or a field a model did not emit, renders empty instead of
	// killing the run mid-flight.
	"stepval": func(root any, path ...string) any {
		cur := root
		for _, key := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				return ""
			}
			if cur, ok = m[key]; !ok {
				return ""
			}
		}
		if cur == nil {
			return ""
		}
		return cur
	},
	"default": func(fallback, v any) any {
		if v == nil || v == "" {
			return fallback
		}
		return v
	},
}

// toStrings flattens any slice into display strings; non-slices become one item.
func toStrings(v any) []string {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return []string{fmt.Sprint(v)}
	}
	out := make([]string, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		item := rv.Index(i).Interface()
		switch item.(type) {
		case map[string]any, []any:
			b, _ := json.Marshal(item)
			out = append(out, string(b))
		default:
			out = append(out, fmt.Sprint(item))
		}
	}
	return out
}

// hyphenPath rewrites dotted paths whose segments contain hyphens — step ids like
// `validate-bug` are natural in YAML but are not valid Go template field names.
// `.Steps.validate-bug.summary` becomes `index .Steps "validate-bug" "summary"`.
var hyphenPath = regexp.MustCompile(`\.Steps\.([A-Za-z0-9_-]+)((?:\.[A-Za-z0-9_-]+)*)`)

func rewriteStepPaths(text string) string {
	return hyphenPath.ReplaceAllStringFunc(text, func(m string) string {
		parts := strings.Split(strings.TrimPrefix(m, ".Steps."), ".")
		var b strings.Builder
		b.WriteString("(stepval .Steps")
		for _, p := range parts {
			fmt.Fprintf(&b, " %q", p)
		}
		b.WriteString(")")
		return b.String()
	})
}

func Render(text string, data TemplateData) (string, error) {
	t, err := template.New("p").Funcs(funcs).Option("missingkey=zero").Parse(rewriteStepPaths(text))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
