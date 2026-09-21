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

// Decide is the step's classifier pass: typed questions answered by a small
// model before the agent starts, with gates that can route or halt cheaply.
type Decide struct {
	// State is the text handed to the classifier (templated like a prompt).
	State     string              `yaml:"state" json:"state"`
	Questions map[string]Question `yaml:"questions" json:"questions"`
	Gates     []DecideGate        `yaml:"gates,omitempty" json:"gates,omitempty"`
}

type Step struct {
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
	// AskHuman grants the `question` built-in and makes a suspension durable:
	// the run parks in needs_input until a human answers.
	AskHuman bool `yaml:"ask_human,omitempty" json:"askHuman,omitempty"`
}

type Definition struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description"`
	InputSchema map[string]any `yaml:"input_schema" json:"inputSchema"`
	Steps       []Step         `yaml:"steps" json:"steps"`
	Path        string         `yaml:"-" json:"path"`
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
}

func (d *Definition) validate(cat Catalog) error {
	if d.Name == "" {
		return fmt.Errorf("workflow name is required")
	}
	seen := map[string]bool{}
	for i, s := range d.Steps {
		if s.ID == "" || s.Prompt == "" {
			return fmt.Errorf("%s: step %d needs id and prompt", d.Name, i)
		}
		if seen[s.ID] {
			return fmt.Errorf("%s: duplicate step id %q", d.Name, s.ID)
		}
		seen[s.ID] = true
		if s.OutputSchema == nil {
			return fmt.Errorf("%s: step %q needs output_schema", d.Name, s.ID)
		}
		if cat != nil {
			if miss := cat.Missing(s.Skills); len(miss) > 0 {
				return fmt.Errorf("%s/%s: skills not in the registry: %s", d.Name, s.ID, strings.Join(miss, ", "))
			}
			if miss := cat.MissingBuiltins(s.Tools); len(miss) > 0 {
				return fmt.Errorf("%s/%s: not built-in tools: %s", d.Name, s.ID, strings.Join(miss, ", "))
			}
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
	return nil
}

// LoadDir reads every *.yaml / *.yml in dir.
func LoadDir(dir string, cat Catalog) (map[string]*Definition, error) {
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
