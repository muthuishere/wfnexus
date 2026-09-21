package engine

import "testing"

func TestValidateJSONReportsEveryFailure(t *testing.T) {
	s, err := compileSchema("t", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"reproduced", "method"},
		"properties": map[string]any{
			"reproduced": map[string]any{"type": "boolean"},
			"method":     map[string]any{"type": "string", "enum": []any{"failing_test", "script"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSON(s, map[string]any{"reproduced": true, "method": "failing_test"}); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	err = validateJSON(s, map[string]any{"reproduced": "yes", "method": "vibes"})
	if err == nil {
		t.Fatal("invalid payload accepted")
	}
	// the message is fed back to the model, so it must name both problems
	msg := err.Error()
	for _, want := range []string{"reproduced", "method"} {
		if !contains(msg, want) {
			t.Fatalf("error %q does not mention %q", msg, want)
		}
	}
}

func TestValidateJSONMissingRequired(t *testing.T) {
	s, _ := compileSchema("t", map[string]any{
		"type":     "object",
		"required": []any{"pr_url"},
		"properties": map[string]any{
			"pr_url": map[string]any{"type": "string"},
		},
	})
	if err := validateJSON(s, map[string]any{}); err == nil {
		t.Fatal("missing required field accepted")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
