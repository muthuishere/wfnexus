package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Memory is the `memory:` key, on a workflow or on one step.
//
// WORKFLOW MEMORY IS ON BY DEFAULT. Every finished run leaves a record and
// every agent step of the next run reads the records back (engine/memory.go).
// The key exists to turn it off or to widen it:
//
//	memory: false
//	memory:
//	  include: [workflow:triage, workflow:other-repo/deploy, project, global]
//	  budget: 2000   # tokens of memory one step may be given
//	  retain: 30     # raw run records kept before folding into the digest
//
// A step's `memory:` narrows the workflow's — it may switch memory off or take
// a smaller include list or budget — and never switches on what the workflow
// switched off.
type Memory struct {
	Off     bool     `yaml:"-" json:"-"`
	Include []string `yaml:"include,omitempty" json:"include,omitempty"`
	Budget  int      `yaml:"budget,omitempty" json:"budget,omitempty"`
	Retain  int      `yaml:"retain,omitempty" json:"retain,omitempty"`
}

type memoryFields struct {
	Include []string `yaml:"include,omitempty" json:"include,omitempty"`
	Budget  int      `yaml:"budget,omitempty" json:"budget,omitempty"`
	Retain  int      `yaml:"retain,omitempty" json:"retain,omitempty"`
}

func (m *Memory) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var on bool
		if err := n.Decode(&on); err != nil {
			return fmt.Errorf("memory: want true, false or a mapping, got %q", n.Value)
		}
		*m = Memory{Off: !on}
		return nil
	}
	var f memoryFields
	if err := n.Decode(&f); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	*m = Memory{Include: f.Include, Budget: f.Budget, Retain: f.Retain}
	return nil
}

func (m Memory) MarshalYAML() (any, error) {
	if m.Off {
		return false, nil
	}
	return memoryFields{Include: m.Include, Budget: m.Budget, Retain: m.Retain}, nil
}

func (m *Memory) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "true" || s == "false" {
		*m = Memory{Off: s == "false"}
		return nil
	}
	var f memoryFields
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	*m = Memory{Include: f.Include, Budget: f.Budget, Retain: f.Retain}
	return nil
}

func (m Memory) MarshalJSON() ([]byte, error) {
	if m.Off {
		return []byte("false"), nil
	}
	return json.Marshal(memoryFields{Include: m.Include, Budget: m.Budget, Retain: m.Retain})
}

// MemorySource is one parsed `include:` entry.
type MemorySource struct {
	Kind     string // "workflow", "project", "global"
	Project  string // workflow kind: "" means the run's own project
	Workflow string
}

// ParseMemorySource reads `workflow:<name>`, `workflow:<project>/<name>`,
// `project` or `global`.
func ParseMemorySource(s string) (MemorySource, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "project", "global":
		return MemorySource{Kind: s}, nil
	}
	rest, ok := strings.CutPrefix(s, "workflow:")
	if !ok || rest == "" {
		return MemorySource{}, fmt.Errorf("memory include %q: want workflow:<name>, workflow:<project>/<name>, project or global", s)
	}
	proj, name, cross := strings.Cut(rest, "/")
	if !cross {
		return MemorySource{Kind: "workflow", Workflow: rest}, nil
	}
	if proj == "" || name == "" || strings.Contains(name, "/") {
		return MemorySource{}, fmt.Errorf("memory include %q: want workflow:<project>/<name>", s)
	}
	return MemorySource{Kind: "workflow", Project: proj, Workflow: name}, nil
}

func (m *Memory) validate(where string) error {
	if m == nil {
		return nil
	}
	if m.Budget < 0 || m.Retain < 0 {
		return fmt.Errorf("%s: memory budget and retain must not be negative", where)
	}
	for _, inc := range m.Include {
		if _, err := ParseMemorySource(inc); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	return nil
}

// Memory defaults.
const (
	DefaultMemoryBudget = 1500 // tokens
	DefaultMemoryRetain = 20   // raw run records
)

// MemoryPlan is the effective memory setting for one step of one workflow.
type MemoryPlan struct {
	On      bool
	Include []string
	Budget  int // tokens
	Retain  int
}

// EffectiveMemory resolves the workflow's and the step's `memory:` into one
// plan. step may be nil (the workflow-level plan).
func EffectiveMemory(def *Definition, step *Step) MemoryPlan {
	p := MemoryPlan{On: true, Budget: DefaultMemoryBudget, Retain: DefaultMemoryRetain}
	if def != nil && def.Memory != nil {
		w := def.Memory
		if w.Off {
			return MemoryPlan{}
		}
		p.Include = w.Include
		if w.Budget > 0 {
			p.Budget = w.Budget
		}
		if w.Retain > 0 {
			p.Retain = w.Retain
		}
	}
	if step != nil && step.Memory != nil {
		s := step.Memory
		if s.Off {
			p.On = false
			return p
		}
		// Narrowing only: the step's include replaces the workflow's when given,
		// but only with entries the workflow also allows.
		if s.Include != nil {
			allowed := map[string]bool{}
			for _, i := range p.Include {
				allowed[strings.TrimSpace(i)] = true
			}
			var keep []string
			for _, i := range s.Include {
				if allowed[strings.TrimSpace(i)] {
					keep = append(keep, i)
				}
			}
			p.Include = keep
		}
		if s.Budget > 0 && s.Budget < p.Budget {
			p.Budget = s.Budget
		}
	}
	return p
}
