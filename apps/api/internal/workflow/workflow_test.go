package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// --- loading and validation against the registry ---

type fakeCatalog struct {
	skills      map[string]bool
	builtins    map[string]bool
	providers   map[string]bool
	classifiers map[string]bool
	mcp         map[string]bool
}

func (c fakeCatalog) Missing(names []string) []string        { return missingIn(names, c.skills) }
func (c fakeCatalog) MissingBuiltins(n []string) []string    { return missingIn(n, c.builtins) }
func (c fakeCatalog) MissingProviders(n []string) []string   { return missingIn(n, c.providers) }
func (c fakeCatalog) MissingClassifiers(n []string) []string { return missingIn(n, c.classifiers) }
func (c fakeCatalog) MissingMcp(n []string) []string         { return missingIn(n, c.mcp) }

func missingIn(names []string, have map[string]bool) []string {
	var out []string
	for _, n := range names {
		if !have[n] {
			out = append(out, n)
		}
	}
	return out
}

func catalog() fakeCatalog {
	return fakeCatalog{
		skills:      map[string]bool{"fix-author": true, "pr-reviewer": true},
		builtins:    map[string]bool{"bash": true, "read": true, "edit": true},
		providers:   map[string]bool{"sonnet": true, "devin": true},
		classifiers: map[string]bool{"jev": true},
		mcp:         map[string]bool{"github": true},
	}
}

func writeWorkflow(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validWorkflow = `
name: demo
description: a demo
steps:
  - id: fix
    prompt: do it
    skills: [fix-author]
    tools: [bash, edit]
    output_schema:
      type: object
      required: [ok]
      properties:
        ok: { type: boolean }
`

func TestLoadDirAcceptsAValidWorkflow(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "demo.yaml", validWorkflow)
	defs, err := LoadDir(dir, catalog())
	if err != nil {
		t.Fatal(err)
	}
	d := defs["demo"]
	if d == nil || len(d.Steps) != 1 {
		t.Fatalf("defs = %+v", defs)
	}
	if d.Steps[0].MaxTurns == 0 || d.Steps[0].MaxAttempts == 0 {
		t.Fatal("defaults were not applied")
	}
	if d.Steps[0].Name != "fix" {
		t.Fatalf("name defaulting: %q", d.Steps[0].Name)
	}
}

func TestLoadDirRejectsUnknownSkill(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "bad.yaml", strings.Replace(validWorkflow, "[fix-author]", "[fix-author, ghost-skill]", 1))
	_, err := LoadDir(dir, catalog())
	if err == nil || !strings.Contains(err.Error(), "ghost-skill") {
		t.Fatalf("err = %v, want a complaint naming ghost-skill", err)
	}
}

func TestLoadDirRejectsUnknownTool(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "bad.yaml", strings.Replace(validWorkflow, "[bash, edit]", "[bash, shell]", 1))
	_, err := LoadDir(dir, catalog())
	if err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("err = %v, want a complaint naming shell", err)
	}
}

func TestLoadDirRejectsStructuralMistakes(t *testing.T) {
	cases := map[string]string{
		"missing output_schema": "name: x\nsteps:\n  - id: a\n    prompt: p\n",
		"missing prompt":        "name: x\nsteps:\n  - id: a\n    output_schema: {type: object}\n",
		"duplicate ids": "name: x\nsteps:\n" +
			"  - {id: a, prompt: p, output_schema: {type: object}}\n" +
			"  - {id: a, prompt: p, output_schema: {type: object}}\n",
		"unknown gate action": "name: x\nsteps:\n  - id: a\n    prompt: p\n    output_schema: {type: object}\n" +
			"    gates: [{field: f, equals: true, action: explode}]\n",
		"gate without field": "name: x\nsteps:\n  - id: a\n    prompt: p\n    output_schema: {type: object}\n" +
			"    gates: [{equals: true, action: fail}]\n",
		"skip_to nowhere": "name: x\nsteps:\n  - id: a\n    prompt: p\n    output_schema: {type: object}\n" +
			"    gates: [{field: f, equals: true, action: skip_to, skip_to: ghost}]\n",
		"no name": "steps:\n  - {id: a, prompt: p, output_schema: {type: object}}\n",
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			writeWorkflow(t, dir, "x.yaml", body)
			if _, err := LoadDir(dir, catalog()); err == nil {
				t.Fatalf("%s was accepted", label)
			}
		})
	}
}

func TestLoadDirAllowsForwardSkipTo(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "x.yaml", "name: x\nsteps:\n"+
		"  - {id: a, prompt: p, output_schema: {type: object}, gates: [{field: f, equals: true, action: skip_to, skip_to: c}]}\n"+
		"  - {id: b, prompt: p, output_schema: {type: object}}\n"+
		"  - {id: c, prompt: p, output_schema: {type: object}}\n")
	if _, err := LoadDir(dir, catalog()); err != nil {
		t.Fatalf("forward skip_to rejected: %v", err)
	}
}

func TestLoadDirIgnoresNonYAML(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "demo.yaml", validWorkflow)
	writeWorkflow(t, dir, "notes.md", "not a workflow")
	writeWorkflow(t, dir, "README.txt", "also not")
	defs, err := LoadDir(dir, catalog())
	if err != nil || len(defs) != 1 {
		t.Fatalf("defs = %d, err = %v", len(defs), err)
	}
}

func TestStepLookup(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "demo.yaml", validWorkflow)
	defs, _ := LoadDir(dir, catalog())
	pos, step := defs["demo"].Step("fix")
	if pos != 0 || step == nil {
		t.Fatalf("Step() = %d, %v", pos, step)
	}
	if pos, step := defs["demo"].Step("nope"); pos != -1 || step != nil {
		t.Fatalf("unknown step returned %d, %v", pos, step)
	}
}

func TestRenderMissingFieldsDoNotExplode(t *testing.T) {
	// a prompt referencing a step that has not run yet must not kill the run
	out, err := Render(`before {{ .Steps.later.value }} after`, TemplateData{Steps: map[string]any{}})
	if err != nil {
		t.Fatalf("missing key errored: %v", err)
	}
	if !strings.Contains(out, "before") {
		t.Fatalf("render = %q", out)
	}
}

func TestRenderSkippedStepRendersEmptyNotError(t *testing.T) {
	// a skip_to gate can bypass a step a later prompt references
	data := TemplateData{Steps: map[string]any{"ran": map[string]any{"value": "here"}}}
	for _, tmpl := range []string{
		`{{ .Steps.skipped.value }}`,
		`{{ .Steps.ran.absent }}`,
		`{{ .Steps.ran.value }}`,
		`{{ join .Steps.skipped.files ", " }}`,
	} {
		if _, err := Render(tmpl, data); err != nil {
			t.Fatalf("%s errored: %v", tmpl, err)
		}
	}
	got, _ := Render(`[{{ .Steps.skipped.value }}][{{ .Steps.ran.value }}]`, data)
	if got != "[][here]" {
		t.Fatalf("render = %q", got)
	}
}

// A step written in the jobs form is stored under a flattened id ("suite.test"),
// while a template references it the way it is written in the file
// (`.Steps.suite.test.exitCode`). Resolving that path segment by segment finds
// no step called "suite" and renders NOTHING — which is how the shipped
// ci-triage workflow was sending its judge a prompt with the test output
// missing, with no error and no log line.
func TestTemplateResolvesAFlattenedJobStepID(t *testing.T) {
	data := TemplateData{Steps: map[string]any{
		"suite.test":   map[string]any{"exitCode": float64(2), "stdout": "FAIL ./..."},
		"validate-bug": map[string]any{"summary": "flat ids still work"},
	}}
	for _, c := range []struct{ tmpl, want string }{
		{"{{ .Steps.suite.test.exitCode }}", "2"},
		{"{{ .Steps.suite.test.stdout }}", "FAIL ./..."},
		{"{{ .Steps.validate-bug.summary }}", "flat ids still work"},
		// A genuinely absent step stays empty rather than erroring at run time.
		{"{{ .Steps.nope.field }}", ""},
	} {
		got, err := Render(c.tmpl, data)
		if err != nil {
			t.Fatalf("%s: %v", c.tmpl, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.tmpl, got, c.want)
		}
	}
}

// Precedence when both readings exist: a step actually named "a.b" wins over a
// step "a" with a field "b". The flattened id is the specific thing, and it is
// the reading the author wrote down; a field is only reachable under its own
// step. Written as a test because it is a choice, not an accident.
func TestAFlattenedIDBeatsAFieldOfTheSameName(t *testing.T) {
	data := TemplateData{Steps: map[string]any{
		"a":   map[string]any{"b": "the field"},
		"a.b": map[string]any{"c": "the flattened step"},
	}}
	got, err := Render("{{ .Steps.a.b.c }}", data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "the flattened step" {
		t.Errorf("got %q, want the flattened step", got)
	}
}
