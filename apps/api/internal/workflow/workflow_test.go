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

func TestJoinAndListAcceptJSONDecodedSlices(t *testing.T) {
	// step outputs come back from jsonb as []any, never []string
	data := TemplateData{Steps: map[string]any{
		"draft-pr": map[string]any{
			"files_changed": []any{"cart.py", "test_cart.py"},
			"findings":      []any{map[string]any{"file": "cart.py", "severity": "minor"}},
			"empty":         []any{},
		},
	}}
	got, err := Render(`{{ join .Steps.draft-pr.files_changed ", " }}`, data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "cart.py, test_cart.py" {
		t.Fatalf("join got %q", got)
	}
	if got, err = Render(`{{ list .Steps.draft-pr.files_changed }}`, data); err != nil || got != "- cart.py\n- test_cart.py" {
		t.Fatalf("list got %q err %v", got, err)
	}
	if got, err = Render(`{{ list .Steps.draft-pr.empty }}`, data); err != nil || got != "(none)" {
		t.Fatalf("empty list got %q err %v", got, err)
	}
	if got, err = Render(`{{ join .Steps.draft-pr.findings "; " }}`, data); err != nil || got != `{"file":"cart.py","severity":"minor"}` {
		t.Fatalf("object join got %q err %v", got, err)
	}
}
