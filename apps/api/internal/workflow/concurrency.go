package workflow

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Concurrency is a workflow's `concurrency:` key, the simplest useful form of
// GitHub Actions' one:
//
//	concurrency: 1                      # shorthand: one active run of this workflow
//	concurrency:
//	  group: nightly-db                 # default: the workflow's name
//	  cancel-in-progress: false         # true: cancel the older run, start the new one
//
// One active run per group at a time. "Active" is running or queued; a run
// parked on a human (awaiting_approval, needs_input) does NOT hold the group,
// because a pause nobody answers would otherwise stop the workflow forever.
type Concurrency struct {
	Group            string `json:"group,omitempty"`
	CancelInProgress bool   `json:"cancelInProgress,omitempty"`
}

// GroupFor is the group a run of the named workflow belongs to; "" when the
// workflow declares no concurrency.
func (c *Concurrency) GroupFor(workflow string) string {
	if c == nil {
		return ""
	}
	if c.Group != "" {
		return c.Group
	}
	return workflow
}

func (c *Concurrency) fromShorthand(n float64) error {
	if n != 1 {
		return fmt.Errorf("concurrency: the shorthand is `concurrency: 1`, got %v", n)
	}
	*c = Concurrency{}
	return nil
}

func (c *Concurrency) fromMap(m map[string]any) error {
	*c = Concurrency{}
	for k, v := range m {
		switch k {
		case "group":
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("concurrency.group: want a string")
			}
			c.Group = s
		case "cancel-in-progress", "cancel_in_progress", "cancelInProgress":
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("concurrency.cancel-in-progress: want true or false")
			}
			c.CancelInProgress = b
		default:
			return fmt.Errorf("concurrency: unknown key %q (known: group, cancel-in-progress)", k)
		}
	}
	return nil
}

func (c *Concurrency) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var f float64
		if err := n.Decode(&f); err != nil {
			return fmt.Errorf("concurrency: want 1 or a mapping: %w", err)
		}
		return c.fromShorthand(f)
	}
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		return fmt.Errorf("concurrency: want 1 or a mapping: %w", err)
	}
	return c.fromMap(m)
}

// MarshalYAML writes back the shortest form with the same meaning.
func (c Concurrency) MarshalYAML() (any, error) {
	if c.Group == "" && !c.CancelInProgress {
		return 1, nil
	}
	out := map[string]any{}
	if c.Group != "" {
		out["group"] = c.Group
	}
	if c.CancelInProgress {
		out["cancel-in-progress"] = true
	}
	return out, nil
}

func (c *Concurrency) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		return c.fromShorthand(f)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("concurrency: want 1 or an object: %w", err)
	}
	return c.fromMap(m)
}
