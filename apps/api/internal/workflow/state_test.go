package workflow

import "testing"

// The four state namespaces as a template sees them.

func stateData() TemplateData {
	return TemplateData{
		Step:     map[string]string{"cursor": "step-value", "repo.head": "abc123"},
		Workflow: map[string]string{"cursor": "workflow-value", "last_id": "4120"},
		Project:  map[string]string{"cursor": "project-value"},
		Global:   map[string]string{"cursor": "global-value", "tier": "pro"},
	}
}

func TestStateNamespacesRender(t *testing.T) {
	cases := map[string]string{
		"{{ .Step.cursor }}":                              "step-value",
		"{{ .Workflow.cursor }}":                          "workflow-value",
		"{{ .Project.cursor }}":                           "project-value",
		"{{ .Global.cursor }}":                            "global-value",
		"{{ .Workflow.last_id }} then {{ .Global.tier }}": "4120 then pro",
		// A key holding a dot is ONE key, not a path.
		"{{ .Step.repo.head }}": "abc123",
	}
	data := stateData()
	for tmpl, want := range cases {
		got, err := Render(tmpl, data)
		if err != nil || got != want {
			t.Fatalf("%s rendered %q (err %v), want %q", tmpl, got, err, want)
		}
	}
}

// Four namespaces, not a cascade: a key present in a wider scope must NOT
// appear in a narrower one, or a missing watermark would look like a stale one.
func TestStateDoesNotFallBack(t *testing.T) {
	data := stateData()
	for _, tmpl := range []string{"{{ .Step.last_id }}", "{{ .Project.last_id }}", "{{ .Global.last_id }}", "{{ .Step.tier }}"} {
		got, err := Render(tmpl, data)
		if err != nil {
			t.Fatalf("%s: %v", tmpl, err)
		}
		if got != "" {
			t.Fatalf("%s fell back to another scope and rendered %q", tmpl, got)
		}
	}
}

// A missing key renders empty at run time, like the rest of the template data,
// and is an error under the CHECKING render the dry run uses.
func TestStateMissingKey(t *testing.T) {
	data := stateData()
	if got, err := Render("[{{ .Workflow.nope }}]", data); err != nil || got != "[]" {
		t.Fatalf("run-time render of a missing key: %q %v", got, err)
	}
	if _, err := RenderStrict("{{ .Workflow.nope }}", data); err == nil {
		t.Fatal("RenderStrict must name a state key nothing holds")
	}
}

// `.Steps.<id>` is a different thing entirely and must not be caught by the
// state rewriter.
func TestStateRewriteLeavesStepOutputsAlone(t *testing.T) {
	data := stateData()
	data.Steps = map[string]any{"scan-repo": map[string]any{"count": 7}}
	got, err := Render("{{ .Steps.scan-repo.count }} / {{ .Step.cursor }}", data)
	if err != nil || got != "7 / step-value" {
		t.Fatalf("got %q (err %v)", got, err)
	}
}
