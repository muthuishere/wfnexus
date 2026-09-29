package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

var outputs = map[string]json.RawMessage{
	"survey": json.RawMessage(`{"severity":"high","finding":"mock finding","count":3,
		"areas":[{"name":"api"},{"name":"ui"}],"has_tests":true,"meta":{"k":1}}`),
	"empty": json.RawMessage(`null`),
}

func TestEveryOperatorPassesAndFailsOnTheTypedOutput(t *testing.T) {
	cases := []struct {
		a    Assertion
		pass bool
	}{
		{Assertion{Step: "survey", Path: "severity", Op: "eq", Value: "high"}, true},
		{Assertion{Step: "survey", Path: "severity", Op: "eq", Value: "low"}, false},
		{Assertion{Step: "survey", Path: "count", Op: "eq", Value: 3}, true}, // int vs float64
		{Assertion{Step: "survey", Path: "severity", Op: "ne", Value: "low"}, true},
		{Assertion{Step: "survey", Path: "finding", Op: "contains", Value: "mock"}, true},
		{Assertion{Step: "survey", Path: "areas", Op: "contains", Value: map[string]any{"name": "ui"}}, true},
		{Assertion{Step: "survey", Path: "meta", Op: "contains", Value: "k"}, true},
		{Assertion{Step: "survey", Path: "finding", Op: "matches", Value: "^mock\\b"}, true},
		{Assertion{Step: "survey", Path: "finding", Op: "matches", Value: "^real"}, false},
		{Assertion{Step: "survey", Path: "areas.#", Op: "gte", Value: 2}, true},
		{Assertion{Step: "survey", Path: "areas.#", Op: "lte", Value: 1}, false},
		{Assertion{Step: "survey", Path: "areas.1.name", Op: "eq", Value: "ui"}, true},
		{Assertion{Step: "survey", Path: "has_tests", Op: "eq", Value: true}, true},
		{Assertion{Step: "survey", Path: "severity", Op: "in", Value: []any{"low", "high"}}, true},
		{Assertion{Step: "survey", Path: "severity", Op: "in", Value: []any{"low"}}, false},
		{Assertion{Step: "survey", Path: "finding", Op: "exists"}, true},
		{Assertion{Step: "survey", Path: "nope", Op: "exists"}, false},
		{Assertion{Step: "survey", Path: "nope", Op: "exists", Value: false}, true},
		{Assertion{Step: "survey", Path: "nope", Op: "eq", Value: nil}, false},
		{Assertion{Step: "survey", Path: "severity", Op: "gte", Value: 1}, false}, // not a number
	}
	for _, c := range cases {
		c.a.Value = normalize(c.a.Value)
		if err := c.a.validate(); err != nil {
			t.Fatalf("%+v: %v", c.a, err)
		}
		if got := Check(c.a, outputs); got.Pass != c.pass {
			t.Errorf("%s %s %v: pass=%v want %v (%s)", c.a.Path, c.a.Op, c.a.Value, got.Pass, c.pass, got.Detail)
		}
	}
}

// A step that never submitted cannot pass an assertion by accident — the
// whole point is that schema validity was not reached, let alone correctness.
func TestAStepWithNoOutputFailsEverythingButAbsence(t *testing.T) {
	for _, step := range []string{"empty", "never-ran"} {
		if Check(Assertion{Step: step, Op: "exists"}, outputs).Pass {
			t.Errorf("%s: exists passed on no output", step)
		}
		if Check(Assertion{Step: step, Op: "ne", Value: "x"}, outputs).Pass {
			t.Errorf("%s: ne passed on no output", step)
		}
		if !Check(Assertion{Step: step, Op: "exists", Value: false}, outputs).Pass {
			t.Errorf("%s: exists:false should pass on no output", step)
		}
	}
}

func TestParseCorpusValidatesBeforeAnythingRuns(t *testing.T) {
	good := `
workflow: mock-demo
cases:
  - name: one
    input: {n: 2, nested: {a: [1, 2]}}
    status: awaiting_approval
    assert:
      - {step: survey, path: severity, op: in, value: [low, medium, high]}
      - {step: survey, path: finding, op: matches, value: "mock"}
  - assert: [{step: survey, op: exists}]
`
	c, err := ParseCorpus([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Cases[1].Name != "case-2" || c.Cases[1].ExpectedStatus() != "done" {
		t.Errorf("defaults not applied: %+v", c.Cases[1])
	}
	if c.Cases[0].Input["n"] != float64(2) {
		t.Errorf("input not normalised to JSON numbers: %#v", c.Cases[0].Input)
	}

	for want, bad := range map[string]string{
		"workflow":       `cases: [{assert: []}]`,
		"no cases":       `workflow: w`,
		"unknown op":     "workflow: w\ncases: [{assert: [{step: s, op: approx}]}]",
		"regexp":         "workflow: w\ncases: [{assert: [{step: s, op: matches, value: \"(\"}]}]",
		"takes a list":   "workflow: w\ncases: [{assert: [{step: s, op: in, value: x}]}]",
		"takes a number": "workflow: w\ncases: [{assert: [{step: s, op: gte, value: x}]}]",
		"step":           "workflow: w\ncases: [{assert: [{op: exists}]}]",
		"appears twice":  "workflow: w\ncases: [{name: a}, {name: a}]",
	} {
		_, err := ParseCorpus([]byte(bad))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", want, err)
		}
	}
}
