package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadStart(t *testing.T, body string) (*Definition, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "w.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	defs, err := LoadDir(dir, nil)
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		return d, nil
	}
	return nil, fmt.Errorf("nothing loaded")
}

// ADR 0026: `start:` takes a name, a mapping or a list.
func TestStartStepShapesLoad(t *testing.T) {
	d, err := loadStart(t, `name: parent
steps:
  - id: one
    start: child
  - id: two
    start: {workflow: child, wait: true, input: {chat: "{{ .Input.chat }}"}}
  - id: three
    start: {workflow: child, continue: event}
  - id: four
    start:
      - {workflow: a, wait: true}
      - {workflow: b, wait: true}
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		mode string
		n    int
		list bool
	}{{"nowait", 1, false}, {"wait", 1, false}, {"event", 1, false}, {"wait", 2, true}}
	for i, w := range want {
		s := d.Steps[i].Start
		if s == nil || s.Mode() != w.mode || len(s.Targets) != w.n || s.List != w.list {
			t.Fatalf("step %d: %+v, want %+v", i, s, w)
		}
		if d.Steps[i].OutputSchema == nil {
			t.Fatalf("step %d has no output schema", i)
		}
	}
	if d.Steps[1].Start.Targets[0].Input["chat"] != "{{ .Input.chat }}" {
		t.Fatalf("input not kept: %+v", d.Steps[1].Start.Targets[0].Input)
	}
	// The JSON form round-trips (the UI saves definitions as JSON).
	raw, _ := json.Marshal(d.Steps[3])
	var back Step
	if err := json.Unmarshal(raw, &back); err != nil || back.Start == nil || len(back.Start.Targets) != 2 || !back.Start.List {
		t.Fatalf("json round trip: %v %+v", err, back.Start)
	}
	var fromMap Step
	if err := json.Unmarshal([]byte(`{"id":"x","start":{"workflow":"c","wait":true}}`), &fromMap); err != nil || fromMap.Start.Mode() != "wait" {
		t.Fatalf("json mapping: %v %+v", err, fromMap.Start)
	}
}

func TestStartStepIsRefusedWhenWrong(t *testing.T) {
	var many []string
	for i := 0; i < MaxStartFanOut+1; i++ {
		many = append(many, fmt.Sprintf("      - {workflow: w%d}", i))
	}
	cases := map[string]string{
		"exactly one of": `name: p
steps:
  - id: s
    run: echo hi
    start: child
`,
		"pick one": `name: p
steps:
  - id: s
    start: {workflow: child, wait: true, continue: event}
`,
		"same mode": `name: p
steps:
  - id: s
    start:
      - {workflow: a, wait: true}
      - {workflow: b}
`,
		"at most 20": "name: p\nsteps:\n  - id: s\n    start:\n" + strings.Join(many, "\n") + "\n",
		"want `event`": `name: p
steps:
  - id: s
    start: {workflow: child, continue: later}
`,
		"needs a `workflow`": `name: p
steps:
  - id: s
    start: {wait: true}
`,
		"fire and forget": `name: p
steps:
  - id: each
    foreach: "[1,2]"
    steps:
      - id: s
        start: {workflow: child, wait: true}
`,
	}
	for want, body := range cases {
		if _, err := loadStart(t, body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

