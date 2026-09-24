package workflow

import (
	"encoding/json"
	"strings"
)

// A definition arrives in two dialects and both are legitimate.
//
//	a file:  output_schema, max_turns, requires_approval, input_schema
//	the API: outputSchema,  maxTurns,  requiresApproval,  inputSchema
//
// The struct carries a yaml tag and a json tag for every field, so which
// dialect is understood depends entirely on which decoder runs — and that made
// the platform quietly inconsistent: the authoring tools read the file dialect,
// while `PUT /api/workflows/{name}` and `POST /api/dryrun` read the API's.
//
// The cost was a whole class of confusing failure. An agent wrote a perfectly
// correct step with `output_schema`, the JSON decoder dropped the key because
// it wanted `outputSchema`, and the platform reported the step as having no
// output schema at all — blaming the author for a field they had written
// correctly. It survived three live authoring runs before being understood.
//
// So: accept both, everywhere. Keys are normalised to the API dialect and
// decoded once. Nothing has to know which dialect its caller used.

// DecodeDefinition reads a definition in either dialect.
func DecodeDefinition(raw []byte) (*Definition, error) {
	var loose map[string]any
	if err := json.Unmarshal(raw, &loose); err != nil {
		return nil, err
	}
	normalised, err := json.Marshal(camelKeys(loose))
	if err != nil {
		return nil, err
	}
	var def Definition
	if err := json.Unmarshal(normalised, &def); err != nil {
		return nil, err
	}
	return &def, nil
}

// camelKeys rewrites snake_case keys to camelCase, leaving everything else
// alone.
//
// It deliberately does NOT descend into a value that is a JSON Schema —
// `output_schema`, `input_schema` and a schema's `properties` hold USER data,
// where a key named `max_turns` is a field the workflow author meant to have
// exactly that name. Renaming it would corrupt the contract the schema
// describes.
func camelKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			key := camel(k)
			if isSchemaKey(key) {
				out[key] = val // opaque: user data, never rewritten
				continue
			}
			out[key] = camelKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = camelKeys(item)
		}
		return out
	}
	return v
}

// isSchemaKey marks a value whose KEYS are user data rather than field names.
//
// Getting this list wrong is not a cosmetic bug: renaming a key inside one of
// these corrupts the author's own vocabulary. The first version missed
// `questions`, and a judge question the author had called `is_security` was
// rewritten to `isSecurity` — so the gate that referenced it by its real name
// stopped resolving and `bug-fix` no longer loaded. Everything here is opaque.
func isSchemaKey(key string) bool {
	switch key {
	// JSON Schema: every property name belongs to the workflow author.
	case "outputSchema", "inputSchema", "properties", "definitions", "$defs":
		return true
	// The judge's vocabulary: question ids, and a choice question's option ids.
	case "questions", "options":
		return true
	// Free-form payloads: a task's overrides, a run's input, an inbound event,
	// environment and headers. All of these hold names chosen elsewhere.
	case "with", "input", "clientPayload", "env", "headers", "mcpServers":
		return true
	// A step's `state:` block: the inner keys are STATE KEY NAMES the author
	// chose, and `last_id` must not become `lastId` — the template that reads
	// it back says `.Workflow.last_id`.
	case "state":
		return true
	}
	return false
}

func camel(s string) string {
	if !strings.Contains(s, "_") || strings.HasPrefix(s, "_") {
		return s
	}
	parts := strings.Split(s, "_")
	var b strings.Builder
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}
