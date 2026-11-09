package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Memory is the `memory:` key, on a workflow or on one step. Workflow memory
// (engine/memory.go — toolnexus's MEMORY.md index + notes + dreaming) is ON by
// default; the key only turns it off:
//
//	memory: false
type Memory struct {
	Off bool `yaml:"-" json:"-"`
}

func (m *Memory) UnmarshalYAML(n *yaml.Node) error {
	var on bool
	if n.Kind != yaml.ScalarNode || n.Decode(&on) != nil {
		return fmt.Errorf("memory: want true or false")
	}
	*m = Memory{Off: !on}
	return nil
}

func (m Memory) MarshalYAML() (any, error) { return !m.Off, nil }

func (m *Memory) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s != "true" && s != "false" {
		return fmt.Errorf("memory: want true or false")
	}
	*m = Memory{Off: s == "false"}
	return nil
}

func (m Memory) MarshalJSON() ([]byte, error) { return json.Marshal(!m.Off) }

func (m *Memory) validate(string) error { return nil }
