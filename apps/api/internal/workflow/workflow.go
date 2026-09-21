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

func (d *Definition) validate() error {
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
		for _, g := range s.Gates {
			switch g.Action {
			case "needs_input", "fail", "skip_to":
			default:
				return fmt.Errorf("%s/%s: unknown gate action %q", d.Name, s.ID, g.Action)
			}
		}
	}
	return nil
}

// LoadDir reads every *.yaml / *.yml in dir.
func LoadDir(dir string) (map[string]*Definition, error) {
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
		if err := d.validate(); err != nil {
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
	Input   map[string]any // run input
	Steps   map[string]any // previous step outputs, keyed by step id
	Output  map[string]any // current step output (gate messages only)
}

var funcs = template.FuncMap{
	"json": func(v any) string {
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	},
	"join": strings.Join,
}

// hyphenPath rewrites dotted paths whose segments contain hyphens — step ids like
// `validate-bug` are natural in YAML but are not valid Go template field names.
// `.Steps.validate-bug.summary` becomes `index .Steps "validate-bug" "summary"`.
var hyphenPath = regexp.MustCompile(`\.Steps\.([A-Za-z0-9_-]+)((?:\.[A-Za-z0-9_-]+)*)`)

func rewriteStepPaths(text string) string {
	return hyphenPath.ReplaceAllStringFunc(text, func(m string) string {
		parts := strings.Split(strings.TrimPrefix(m, ".Steps."), ".")
		var b strings.Builder
		b.WriteString("(index .Steps")
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
