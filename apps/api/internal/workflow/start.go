package workflow

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// StartStep is a `start:` step (ADR 0026 decision 5): it starts another
// workflow as an INDEPENDENT run of its own, recorded with this run as its
// parent.
//
//   - id: hand-off
//     start:
//     workflow: whatsapp-sender
//     input: { chat: "{{ .Input.message.chat }}", text: "{{ .Steps.reply.text }}" }
//     wait: true            # pause this run until the child ends; its output is this step's
//
//   - id: later
//     start: { workflow: long-job, continue: event }   # finish now; the child's end is an event
//
//   - id: fan-out
//     start:                # a list starts several at once
//
//   - { workflow: a, wait: true }
//
//   - { workflow: b, wait: true }
//
// Modes: `wait: false` (the default) starts and moves on; `wait: true` waits
// for the child; `continue: event` ends this run now and the child's end comes
// back as a `workflow.completed` event in the same conversation. Every target
// of a list shares one mode. Limits: MaxStartDepth levels, MaxStartFanOut
// children per run.
type StartStep struct {
	Targets []StartTarget `yaml:"-" json:"targets"`
	// List is true when the YAML wrote a list — its output is the aggregate
	// form even for one item.
	List bool `yaml:"-" json:"list,omitempty"`
}

// StartTarget is one workflow a `start:` step starts. Workflow and every
// string in Input are templates over the same data a prompt sees.
type StartTarget struct {
	Workflow string         `yaml:"workflow" json:"workflow"`
	Input    map[string]any `yaml:"input,omitempty" json:"input,omitempty"`
	Wait     bool           `yaml:"wait,omitempty" json:"wait,omitempty"`
	// Continue is "" or "event".
	Continue string `yaml:"continue,omitempty" json:"continue,omitempty"`
}

// Limits on `start:` (ADR 0026).
const (
	MaxStartDepth  = 5  // a run plus its ancestors
	MaxStartFanOut = 20 // children one run may start
)

// Mode is the step's one mode: "wait", "event" or "nowait".
func (s *StartStep) Mode() string {
	if s == nil || len(s.Targets) == 0 {
		return "nowait"
	}
	t := s.Targets[0]
	switch {
	case t.Continue == "event":
		return "event"
	case t.Wait:
		return "wait"
	}
	return "nowait"
}

func (s *StartStep) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		var ts []StartTarget
		if err := n.Decode(&ts); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		*s = StartStep{Targets: ts, List: true}
	case yaml.MappingNode:
		var t StartTarget
		if err := n.Decode(&t); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		*s = StartStep{Targets: []StartTarget{t}}
	case yaml.ScalarNode:
		// `start: other-workflow` — the shortest form.
		*s = StartStep{Targets: []StartTarget{{Workflow: n.Value}}}
	default:
		return fmt.Errorf("start: want a workflow name, a mapping or a list")
	}
	return nil
}

func (s StartStep) MarshalYAML() (any, error) {
	if !s.List && len(s.Targets) == 1 {
		return s.Targets[0], nil
	}
	return s.Targets, nil
}

// UnmarshalJSON takes the same three shapes as the YAML, plus the
// {targets, list} form MarshalJSON writes.
func (s *StartStep) UnmarshalJSON(b []byte) error {
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = *n.Content[0]
	}
	if n.Kind == yaml.MappingNode {
		var probe struct {
			Targets []StartTarget `yaml:"targets"`
			List    bool          `yaml:"list"`
		}
		if err := n.Decode(&probe); err == nil && len(probe.Targets) > 0 {
			*s = StartStep{Targets: probe.Targets, List: probe.List}
			return nil
		}
	}
	return s.UnmarshalYAML(&n)
}

func (s *StartStep) validate() error {
	if len(s.Targets) == 0 {
		return fmt.Errorf("names no workflow")
	}
	if len(s.Targets) > MaxStartFanOut {
		return fmt.Errorf("starts %d workflows; one run may start at most %d", len(s.Targets), MaxStartFanOut)
	}
	mode := s.Mode()
	for i, t := range s.Targets {
		if strings.TrimSpace(t.Workflow) == "" {
			return fmt.Errorf("target %d needs a `workflow`", i+1)
		}
		if t.Continue != "" && t.Continue != "event" {
			return fmt.Errorf("continue: want `event`, got %q", t.Continue)
		}
		if t.Continue == "event" && t.Wait {
			return fmt.Errorf("`wait: true` and `continue: event` are two different modes — pick one")
		}
		one := (&StartStep{Targets: []StartTarget{t}}).Mode()
		if one != mode {
			return fmt.Errorf("every workflow in one `start:` list uses the same mode (wait, continue: event, or neither)")
		}
	}
	return nil
}

// StartOutputSchema is the fixed output of a `start:` step. A single target
// answers with the child's fields; a list answers with `runs`.
func StartOutputSchema() map[string]any {
	child := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"runId":    map[string]any{"type": "string"},
			"workflow": map[string]any{"type": "string"},
			"status":   map[string]any{"type": "string", "description": "started | done | failed | cancelled"},
			"error":    map[string]any{"type": "string"},
			"output":   map[string]any{"description": "the child's final output (wait: true): its last finished step's output"},
		},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"runId":    map[string]any{"type": "string"},
			"workflow": map[string]any{"type": "string"},
			"status":   map[string]any{"type": "string"},
			"error":    map[string]any{"type": "string"},
			"output":   map[string]any{},
			"runs":     map[string]any{"type": "array", "items": child},
		},
	}
}
