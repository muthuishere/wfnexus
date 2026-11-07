package workflow

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Typed inputs for a reusable task (issue #82, the MVP of modules).
//
// A task — a tasks/ file, or a workflow declaring `on: workflow_call` — may
// declare what it takes:
//
//	version: 1
//	inputs:
//	  channel: { type: string, required: true }
//	  limit:   { type: integer, default: 20 }
//	  mode:    { type: string, enum: [brief, full], default: brief }
//	outputs:
//	  context: { type: string, description: path of context.json }
//
// and a user passes VALUES under `inputs:` on the use, beside `with:`:
//
//	uses:
//	  - use: load-context@1
//	    inputs: { channel: ops, limit: 5 }
//
// `with:` keeps its existing meaning — per-step harness overrides keyed by the
// task's step id — so the two never collide and no existing file changes
// meaning. Values are checked at LOAD time (unknown name, missing required,
// wrong type, not in the enum) and substituted into the task's steps wherever
// they write `{{ .Inputs.<name> }}`. Substitution is textual and happens before
// any run-time render, so a value is the AUTHOR's literal (never run-time data)
// and a non-string value is written as JSON.
//
// A workflow that declares inputs and is run on its own (not used) gets its
// defaults substituted at normalize; an input with no default renders empty.

// InputSpec declares one input (JSON-schema-ish).
type InputSpec struct {
	Type        string `yaml:"type,omitempty" json:"type,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Default     any    `yaml:"default,omitempty" json:"default,omitempty"`
	Enum        []any  `yaml:"enum,omitempty" json:"enum,omitempty"`
}

// OutputSpec documents one output. Informational in the MVP.
type OutputSpec struct {
	Type        string `yaml:"type,omitempty" json:"type,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

var inputRef = regexp.MustCompile(`\{\{-?\s*\.Inputs\.([A-Za-z0-9_-]+)\s*-?\}\}`)

// localPin matches `name@<major>` / `name@v<major>`: a pin on a LOCAL task's
// major version. A remote reference always names a repository (`owner/repo@ref`).
var localPin = regexp.MustCompile(`^([A-Za-z0-9_.-]+)@v?([0-9]+)$`)

func splitLocalPin(ref string) (name string, major int, ok bool) {
	m := localPin.FindStringSubmatch(ref)
	if m == nil {
		return ref, 0, false
	}
	n, _ := strconv.Atoi(m[2])
	return m[1], n, true
}

// majorOf reads the major from a task's `version:` ("2", "2.1", "v2.0.1").
func majorOf(v string) (int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.Index(v, "."); i >= 0 {
		v = v[:i]
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

// checkSpecs validates the declarations themselves.
func checkSpecs(owner string, specs map[string]InputSpec) error {
	for _, name := range keysOf(specs) {
		s := specs[name]
		switch s.Type {
		case "", "string", "integer", "number", "boolean", "array", "object":
		default:
			return fmt.Errorf("%s: input %q has unknown type %q (string, integer, number, boolean, array, object)", owner, name, s.Type)
		}
		for _, e := range s.Enum {
			if err := checkType(s.Type, e); err != nil {
				return fmt.Errorf("%s: input %q enum value: %v", owner, name, err)
			}
		}
		if s.Default != nil {
			if err := checkValue(s, s.Default); err != nil {
				return fmt.Errorf("%s: input %q default: %v", owner, name, err)
			}
		}
	}
	return nil
}

// resolveInputs checks the supplied values against the specs and returns the
// full set (defaults filled). where names the workflow and use for errors.
func resolveInputs(where string, specs map[string]InputSpec, given map[string]any) (map[string]any, error) {
	for _, k := range keysOf(given) {
		if _, ok := specs[k]; !ok {
			return nil, fmt.Errorf("%s: unknown input %q — declared: %v", where, k, keysOf(specs))
		}
	}
	out := map[string]any{}
	for _, name := range keysOf(specs) {
		s := specs[name]
		v, ok := given[name]
		if !ok || v == nil {
			if s.Required && s.Default == nil {
				return nil, fmt.Errorf("%s: input %q is required", where, name)
			}
			out[name] = s.Default
			continue
		}
		if err := checkValue(s, v); err != nil {
			return nil, fmt.Errorf("%s: input %q: %v", where, name, err)
		}
		out[name] = v
	}
	return out, nil
}

func checkValue(s InputSpec, v any) error {
	if err := checkType(s.Type, v); err != nil {
		return err
	}
	if len(s.Enum) > 0 {
		for _, e := range s.Enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				return nil
			}
		}
		return fmt.Errorf("%v is not one of %v", v, s.Enum)
	}
	return nil
}

func checkType(typ string, v any) error {
	ok := true
	switch typ {
	case "":
	case "string":
		_, ok = v.(string)
	case "boolean":
		_, ok = v.(bool)
	case "integer":
		switch n := v.(type) {
		case int, int64, uint64:
		case float64:
			ok = n == math.Trunc(n)
		default:
			ok = false
		}
	case "number":
		switch v.(type) {
		case int, int64, uint64, float64:
		default:
			ok = false
		}
	case "array":
		_, ok = v.([]any)
	case "object":
		_, ok = v.(map[string]any)
	}
	if !ok {
		return fmt.Errorf("want %s, got %v (%T)", typ, v, v)
	}
	return nil
}

func inputText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int, int64, uint64, float64, bool:
		return fmt.Sprint(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func substInputs(text string, vals map[string]any) string {
	if !strings.Contains(text, ".Inputs.") {
		return text
	}
	return inputRef.ReplaceAllStringFunc(text, func(m string) string {
		name := inputRef.FindStringSubmatch(m)[1]
		if v, ok := vals[name]; ok {
			return inputText(v)
		}
		return m
	})
}

// applyInputs writes the values into every text a step renders.
func applyInputs(s *Step, vals map[string]any) {
	if len(vals) == 0 {
		return
	}
	s.Name = substInputs(s.Name, vals)
	s.Description = substInputs(s.Description, vals)
	s.Prompt = substInputs(s.Prompt, vals)
	s.System = substInputs(s.System, vals)
	s.Run = substInputs(s.Run, vals)
	if len(s.Env) > 0 {
		env := make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			env[k] = substInputs(v, vals)
		}
		s.Env = env
	}
}

// selfInputDefaults is the stand-alone case: a workflow that declares inputs
// and is run directly, not used — its defaults fill its own references.
func selfInputDefaults(d *Definition) {
	if len(d.Inputs) == 0 {
		return
	}
	vals := map[string]any{}
	for n, s := range d.Inputs {
		vals[n] = s.Default
	}
	for i := range d.Steps {
		applyInputs(&d.Steps[i], vals)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
