// Package eval is the proof of portability (ADR 0019): a recorded set of inputs
// for one workflow, assertions over each step's VALIDATED typed output, and a
// runner that executes the same corpus against several named providers so the
// answer comes back as a matrix rather than as a claim.
//
// An assertion is a predicate over JSON and nothing more. It reads the object a
// step already persisted in `step_runs.output` — never prose, never the
// transcript — so checking it costs a comparison, not another inference. That
// is the dividend of ADR 0003: everyone whose steps emit prose needs a model to
// judge the prose, and that judge is itself nondeterministic across the very
// backends under test.
package eval

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Corpus is one eval file: a workflow and the cases to run it on.
//
//	workflow: mock-demo
//	cases:
//	  - name: reports-a-finding
//	    input: {title: "x"}
//	    status: awaiting_approval
//	    assert:
//	      - {step: survey, path: severity, op: in, value: [low, medium, high]}
type Corpus struct {
	Workflow string `yaml:"workflow" json:"workflow"`
	Cases    []Case `yaml:"cases" json:"cases"`
}

// Case is one input and what must be true of the run it produces.
type Case struct {
	Name  string         `yaml:"name" json:"name"`
	Input map[string]any `yaml:"input" json:"input,omitempty"`
	// Status is the run status the case expects to end in; empty means "done".
	// It exists because a workflow with an approval gate PARKS — the runner
	// never approves anything on anyone's behalf (AGENTS.md invariant 5), so a
	// gated workflow's honest end state in an eval is awaiting_approval, and
	// the assertions cover the steps that ran before the gate.
	Status string      `yaml:"status" json:"status,omitempty"`
	Assert []Assertion `yaml:"assert" json:"assert"`
}

// Assertion is a predicate over one step's typed output.
type Assertion struct {
	Step string `yaml:"step" json:"step"`
	// Path is dotted into the output: `areas.0.name`. A `#` segment is the
	// length of the array, object or string it lands on — gjson's spelling,
	// adopted rather than invented — so `len(areas) >= 2` is
	// `{path: "areas.#", op: gte, value: 2}`. Empty means the whole output.
	Path  string `yaml:"path" json:"path,omitempty"`
	Op    string `yaml:"op" json:"op"`
	Value any    `yaml:"value" json:"value,omitempty"`
}

// ExpectedStatus is the status a case passes with.
func (c Case) ExpectedStatus() string {
	if c.Status == "" {
		return "done"
	}
	return c.Status
}

// LoadCorpus reads and validates a corpus file.
func LoadCorpus(path string) (*Corpus, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCorpus(raw)
}

// ParseCorpus validates up front, so a typo in an operator fails before a
// single paid model call rather than after the whole matrix has run.
func ParseCorpus(raw []byte) (*Corpus, error) {
	var c Corpus
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("corpus: %w", err)
	}
	if c.Workflow == "" {
		return nil, fmt.Errorf("corpus: `workflow` is required")
	}
	if len(c.Cases) == 0 {
		return nil, fmt.Errorf("corpus: no cases")
	}
	seen := map[string]bool{}
	for i := range c.Cases {
		cs := &c.Cases[i]
		if cs.Name == "" {
			cs.Name = fmt.Sprintf("case-%d", i+1)
		}
		if seen[cs.Name] {
			return nil, fmt.Errorf("corpus: case %q appears twice", cs.Name)
		}
		seen[cs.Name] = true
		// YAML maps decode as map[string]any already, but a nested value may
		// not; normalise so the engine receives plain JSON-shaped input.
		if cs.Input != nil {
			cs.Input, _ = normalize(cs.Input).(map[string]any)
		}
		for j := range cs.Assert {
			a := &cs.Assert[j]
			a.Value = normalize(a.Value)
			if err := a.validate(); err != nil {
				return nil, fmt.Errorf("corpus: case %q assertion %d: %w", cs.Name, j+1, err)
			}
		}
	}
	return &c, nil
}

func (a Assertion) validate() error {
	if a.Step == "" {
		return fmt.Errorf("`step` is required")
	}
	switch a.Op {
	case "eq", "ne", "contains":
	case "exists":
		if a.Value != nil {
			if _, ok := a.Value.(bool); !ok {
				return fmt.Errorf("exists takes true, false or nothing")
			}
		}
	case "matches":
		s, ok := a.Value.(string)
		if !ok {
			return fmt.Errorf("matches takes a regular expression string")
		}
		if _, err := regexp.Compile(s); err != nil {
			return fmt.Errorf("matches: %w", err)
		}
	case "gte", "lte":
		if _, ok := number(a.Value); !ok {
			return fmt.Errorf("%s takes a number", a.Op)
		}
	case "in":
		if _, ok := a.Value.([]any); !ok {
			return fmt.Errorf("in takes a list")
		}
	default:
		return fmt.Errorf("unknown op %q (eq, ne, contains, matches, exists, gte, lte, in)", a.Op)
	}
	return nil
}

// normalize turns YAML's decoded shapes into JSON's: every map keyed by
// string, every number a float64. Comparison then has one representation to
// deal with instead of yaml's int/float/map[any]any zoo.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalize(x)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = normalize(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case float32:
		return float64(t)
	}
	return v
}
