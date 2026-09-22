package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// PrepareInput applies a workflow's declared input defaults and then validates
// the result against its input_schema.
//
// Neither happened before. A run's input was stored exactly as posted, so
// `default:` in an input_schema was decoration: a live `local-model` run
// declared a default question, was started without one, and its prompt
// rendered the literal string "<no value>". The agent noticed and said so in
// its output, which is a generous way to find out.
//
// It is the same shape as ADR 0004's lesson and ADR 0006's: a field that
// expresses intent is not a control until something reads it. Defaults are
// filled first and validation runs on the filled value, so a default can
// satisfy `required`.
func (e *Engine) PrepareInput(name string, raw json.RawMessage) (json.RawMessage, error) {
	return e.PrepareRun(name, workflow.TriggerDispatch, raw)
}

// PrepareRun checks the TRIGGER and then the input.
//
// The trigger check is the point: `on:` declares what may start a workflow, and
// a declaration nothing enforces is the failure this project keeps meeting — a
// field that expresses intent is not a control (ADR 0004, ADR 0006, and the
// `provider:` that was validated then ignored). A workflow that lists only
// `schedule:` cannot be started by a person, and one that lists only
// `workflow_dispatch` cannot be started by an inbound POST.
func (e *Engine) PrepareRun(name string, trigger workflow.TriggerKind, raw json.RawMessage) (json.RawMessage, error) {
	def := e.Definitions()[name]
	if def == nil {
		return nil, fmt.Errorf("workflow %q is not loaded", name)
	}
	if !def.On.Allows(trigger) {
		return nil, fmt.Errorf("%s cannot be started by %s — it declares `on: %s`",
			name, trigger, strings.Join(def.On.Names(), ", "))
	}
	if def.Template {
		return nil, fmt.Errorf("%s is a template: copy it and run the copy", name)
	}
	if def.InputSchema == nil {
		return raw, nil
	}
	input := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, fmt.Errorf("input is not a JSON object: %w", err)
		}
	}
	applyDefaults(def.InputSchema, input)

	schema, err := compileSchema(name+".input", def.InputSchema)
	if err != nil {
		// A schema we cannot compile is an authoring bug, not a caller's, and
		// refusing every run over it would be worse than running. Loading
		// already validated it; say nothing and carry the filled input.
		return mustJSON(input), nil
	}
	if err := validateAs(name+" input", schema, input); err != nil {
		return nil, err
	}
	return mustJSON(input), nil
}

// applyDefaults fills top-level properties that declare a default and were not
// supplied. Only the top level: a nested default is not something any workflow
// declares yet, and guessing how deep to go invents behaviour nobody asked for.
func applyDefaults(schema map[string]any, input map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		def, has := p["default"]
		if !has {
			continue
		}
		if _, supplied := input[name]; !supplied {
			input[name] = def
		}
	}
}
