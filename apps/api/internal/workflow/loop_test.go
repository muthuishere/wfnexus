package workflow

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadLoop(t *testing.T, src string) (*Definition, error) {
	t.Helper()
	d := &Definition{}
	if err := yaml.Unmarshal([]byte(src), d); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	normalize(d)
	return d, d.validate(nil)
}

const loopHeader = `
name: loops
input_schema:
  type: object
  properties:
    files: {type: array, items: {type: string}}
    title: {type: string}
`

func TestForeachOverInputLoads(t *testing.T) {
	d, err := loadLoop(t, loopHeader+`
steps:
  - id: review
    foreach: input.files
    parallel: 2
    run: echo {{ .Item }} {{ .Index }}
`)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Steps[0]
	if s.LoopMode() != "foreach" || s.ForeachPath() != "input.files" || s.Parallel != 2 {
		t.Fatalf("loop not read: %+v", s)
	}
	props := s.ResultSchema()["properties"].(map[string]any)
	if _, ok := props["iterations"]; !ok {
		t.Fatalf("aggregate schema lacks iterations: %v", props)
	}
}

func TestUntilGroupLoadsAndRoutesExit(t *testing.T) {
	d, err := loadLoop(t, loopHeader+`
steps:
  - id: fix-loop
    max_iterations: 4
    until:
      - path: check.exitCode
        equals: 0
    steps:
      - id: fix
        run: echo fixing
      - id: check
        run: test -f done
`)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Steps[0].Steps[1].ContinueOnError {
		t.Fatal("a body step whose exit the until guard reads must route, not fail the run")
	}
	if d.Steps[0].Steps[0].OutputSchema == nil {
		t.Fatal("body steps must be normalised (run output schema)")
	}
}

func TestLoopValidationRejects(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"until without bound": {`
steps:
  - id: a
    run: "true"
    until: [{path: a.ok, equals: true}]
`, "needs max_iterations"},
		"while without bound": {`
steps:
  - id: a
    run: "true"
    while: [{path: a.ok, equals: false}]
`, "needs max_iterations"},
		"over the ceiling": {`
steps:
  - id: a
    run: "true"
    max_iterations: 5000
    until: [{path: a.ok, equals: true}]
`, "max_iterations must be"},
		"guard reads nothing the loop makes": {`
steps:
  - id: a
    run: "true"
    max_iterations: 3
    until: [{path: input.title, equals: x}]
`, "must read something the loop produces"},
		"guard names unknown step": {`
steps:
  - id: a
    run: "true"
    max_iterations: 3
    until: [{path: nope.ok, equals: true}]
`, "names no step"},
		"foreach undeclared input": {`
steps:
  - id: a
    run: "true"
    foreach: input.nope
`, "does not declare"},
		"foreach non-array input": {`
steps:
  - id: a
    run: "true"
    foreach: input.title
`, "not an array"},
		"foreach unknown step": {`
steps:
  - id: a
    run: "true"
    foreach: ghost.items
`, "names no step"},
		"foreach later step": {`
steps:
  - id: a
    run: "true"
    foreach: b.stdout
  - id: b
    run: "true"
`, "runs AFTER"},
		"foreach non-array field": {`
steps:
  - id: b
    run: "true"
  - id: a
    run: "true"
    foreach: b.stdout
`, "not an array"},
		"foreach a template": {`
steps:
  - id: a
    run: "true"
    foreach: "{{ .Input.files }}"
`, "is a template"},
		"two modes": {`
steps:
  - id: a
    run: "true"
    foreach: [1, 2]
    max_iterations: 3
    until: [{path: a.ok, equals: true}]
`, "ONE of"},
		"parallel until": {`
steps:
  - id: a
    run: "true"
    parallel: 3
    max_iterations: 3
    until: [{path: a.ok, equals: true}]
`, "parallel applies to foreach"},
		"steps without loop": {`
steps:
  - id: a
    steps:
      - id: b
        run: "true"
`, "loop body"},
		"nested loop": {`
steps:
  - id: a
    foreach: [1]
    steps:
      - id: b
        run: "true"
        foreach: [1]
`, "loop inside a loop"},
		"body shadows outer": {`
steps:
  - id: b
    run: "true"
  - id: a
    foreach: [1]
    steps:
      - id: b
        run: "true"
`, "shadows"},
		"group with a prompt": {`
steps:
  - id: a
    foreach: [1]
    run: "true"
    steps:
      - id: b
        run: "true"
`, "no prompt/run"},
		"bad body step": {`
steps:
  - id: a
    foreach: [1]
    steps:
      - id: b
        prompt: hi
`, "needs output_schema"},
		"bracket in id": {`
steps:
  - id: "a[0]"
    run: "true"
`, "may not contain"},
		"max_iterations without loop": {`
steps:
  - id: a
    run: "true"
    max_iterations: 3
`, "only apply to a loop"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadLoop(t, loopHeader+c.yaml)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestForeachInDAGNeedsItsSource(t *testing.T) {
	src := loopHeader + `
steps:
  - id: list
    prompt: list
    output_schema: {type: object, properties: {items: {type: array}}}
  - id: other
    run: "true"
    needs: [list]
  - id: each
    run: echo {{ .Item }}
    foreach: list.items
    needs: [%s]
`
	if _, err := loadLoop(t, strings.Replace(src, "%s", "other", 1)); err != nil {
		t.Fatalf("a transitive need is enough: %v", err)
	}
	d := &Definition{}
	_ = yaml.Unmarshal([]byte(strings.Replace(src, "needs: [%s]", "needs: []", 1)+"  - id: z\n    run: \"true\"\n    needs: [list]\n"), d)
	normalize(d)
	if err := d.validate(nil); err == nil || !strings.Contains(err.Error(), "does not `needs` it") {
		t.Fatalf("err = %v", err)
	}
}

func TestIterationIDs(t *testing.T) {
	if got := IterationID("review", 2, ""); got != "review[2]" {
		t.Fatal(got)
	}
	p, i, b, ok := SplitIterationID("job.fix-loop[11].check")
	if !ok || p != "job.fix-loop" || i != 11 || b != "check" {
		t.Fatalf("%q %d %q %v", p, i, b, ok)
	}
	if _, _, _, ok := SplitIterationID("plain"); ok {
		t.Fatal("a plain id is not an iteration")
	}
	if IterationPosition(3, 1, 2) <= IterationPosition(3, 0, 99) || IterationPosition(4, 0, 0) <= IterationPosition(3, 998, 99) {
		t.Fatal("iteration positions must order by parent, then iteration, then body")
	}
}

func TestLookupResolvesIterationRows(t *testing.T) {
	d, err := loadLoop(t, loopHeader+`
steps:
  - id: ship
    foreach: [a, b]
    steps:
      - id: deploy
        run: echo {{ .Item }}
        requires_approval: true
        approvers: [alice]
`)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Lookup("ship[1].deploy")
	if s == nil || len(s.Approvers) != 1 || s.Approvers[0] != "alice" {
		t.Fatalf("iteration row must resolve to its body step: %+v", s)
	}
	if d.Lookup("ship[0]") != &d.Steps[0] || d.Lookup("ship[0].nope") != nil {
		t.Fatal("lookup of loop/unknown body wrong")
	}
	if d.Steps[0].ApprovesOnce() {
		t.Fatal("a group loop with no approval of its own asks nothing up front")
	}
}

func TestResolvePathLongestStepID(t *testing.T) {
	steps := map[string]any{"job.list": map[string]any{"items": []any{1, 2}}}
	v, ok := ResolvePath("job.list.items", nil, steps)
	if !ok || len(v.([]any)) != 2 {
		t.Fatalf("%v %v", v, ok)
	}
	v, ok = ResolvePath("input.files", map[string]any{"files": []any{"x"}}, steps)
	if !ok || v.([]any)[0] != "x" {
		t.Fatalf("%v %v", v, ok)
	}
}

func TestRenderSeesItemAndIndex(t *testing.T) {
	out, err := RenderStrict("{{ .Index }}:{{ .Item.name }}", TemplateData{Item: map[string]any{"name": "a.go"}, Index: 3})
	if err != nil || out != "3:a.go" {
		t.Fatalf("%q %v", out, err)
	}
}
