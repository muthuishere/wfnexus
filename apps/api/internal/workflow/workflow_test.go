package workflow

import "testing"

func TestRenderHyphenatedStepIDs(t *testing.T) {
	data := TemplateData{
		Input: map[string]any{"title": "boom"},
		Steps: map[string]any{
			"validate-bug":  map[string]any{"summary": "it breaks", "severity": "high"},
			"reproduce_bug": map[string]any{"method": "failing_test"},
		},
	}
	got, err := Render(`{{ .Input.title }}|{{ .Steps.validate-bug.summary }}|{{ .Steps.validate-bug.severity }}|{{ .Steps.reproduce_bug.method }}`, data)
	if err != nil {
		t.Fatal(err)
	}
	if want := "boom|it breaks|high|failing_test"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderJSONAndJoin(t *testing.T) {
	data := TemplateData{Steps: map[string]any{"a-b": map[string]any{"x": 1}}, Output: map[string]any{}}
	got, err := Render(`{{ json .Steps.a-b }}`, data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "{\n  \"x\": 1\n}" {
		t.Fatalf("got %q", got)
	}
}
