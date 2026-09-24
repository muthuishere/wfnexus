package engine

import (
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The loader used to accept any JSON object as a schema, so `type: nonsense`
// or `required: "x"` became a submit_output tool no submission could satisfy —
// a run that fails on its first turn, for a mistake that was on screen while it
// was being authored. The builder now shows this verdict as it is typed.
func TestCheckSchemasRejectsWhatWillNotCompile(t *testing.T) {
	ok := map[string]any{
		"type":       "object",
		"required":   []any{"summary"},
		"properties": map[string]any{"summary": map[string]any{"type": "string"}},
	}
	for _, tc := range []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{"a good schema", ok, ""},
		{"the run contract", workflow.RunOutputSchema(), ""},
		{"an unknown type", map[string]any{"type": "nonsense"}, "not a valid JSON Schema"},
		{"required is not a list", map[string]any{"type": "object", "required": "x"}, "not a valid JSON Schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &workflow.Definition{
				Name:  "probe",
				Steps: []workflow.Step{{ID: "a", Prompt: "hi", OutputSchema: tc.schema}},
			}
			err := checkSchemas(d)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("wanted it accepted, got %v", err)
			case tc.want != "" && err == nil:
				t.Fatal("wanted it refused, it was accepted")
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("message does not say why: %v", err)
			case tc.want != "" && !strings.Contains(err.Error(), `step "a"`):
				t.Fatalf("message does not say WHICH step: %v", err)
			}
		})
	}
}

func TestCheckSchemasCoversTheInputSchema(t *testing.T) {
	d := &workflow.Definition{Name: "probe", InputSchema: map[string]any{"type": "nonsense"}}
	err := checkSchemas(d)
	if err == nil || !strings.Contains(err.Error(), "input_schema") {
		t.Fatalf("the run form's schema is compiled too: %v", err)
	}
}
