package workflow

import (
	"path/filepath"
	"strings"
	"testing"
)

const typedTask = `
name: greet
version: 2.1
inputs:
  who:   { type: string, required: true }
  times: { type: integer, default: 1 }
  tone:  { type: string, enum: [plain, loud], default: plain }
  tags:  { type: array }
outputs:
  greeting: { type: string }
steps:
  - id: say
    run: echo "{{ .Inputs.who }} x{{.Inputs.times}} {{ .Inputs.tone }} {{ .Inputs.tags }} {{ .Input.raw }}"
`

func loadWithTask(t *testing.T, wf string) (map[string]*Definition, error) {
	t.Helper()
	dir, tasks := t.TempDir(), t.TempDir()
	write(t, filepath.Join(tasks, "greet.yaml"), typedTask)
	write(t, filepath.Join(dir, "wf.yaml"), wf)
	return LoadDirWithTasks(dir, tasks, catalog())
}

func TestTaskInputsAreSubstitutedWithDefaults(t *testing.T) {
	defs, err := loadWithTask(t, `
name: wf
uses:
  - use: greet@2
    inputs: { who: ops, tags: [a, b] }
`)
	if err != nil {
		t.Fatal(err)
	}
	got := filled(t, defs["wf"].Steps[0].Run, map[string]any{"raw": "R"})
	want := `echo "ops x1 plain ["a","b"] R"`
	if got != want {
		t.Fatalf("run = %s\nwant  %s", got, want)
	}
}

func TestTaskInputsInAJobUsesWithOverridesStillWorking(t *testing.T) {
	defs, err := loadWithTask(t, `
name: wf
jobs:
  build:
    uses:
      - use: greet
        inputs: { who: dev, times: 3, tone: loud }
        with:
          "*": { model: tiny }
`)
	if err != nil {
		t.Fatal(err)
	}
	s := defs["wf"].Steps[0]
	if !strings.Contains(filled(t, s.Run, nil), "dev x3 loud") || s.Model != "tiny" {
		t.Fatalf("run=%q model=%q", s.Run, s.Model)
	}
}

func TestTaskInputValidationErrors(t *testing.T) {
	cases := map[string]string{
		`inputs: { times: 2 }`:            `wf job j: use "greet" (as greet): input "who" is required`,
		`inputs: { who: 5 }`:              `input "who": want string`,
		`inputs: { who: x, times: 1.5 }`:  `input "times": want integer`,
		`inputs: { who: x, tone: shout }`: `input "tone": shout is not one of [plain loud]`,
		`inputs: { who: x, nope: 1 }`:     `unknown input "nope"`,
	}
	for in, want := range cases {
		_, err := loadWithTask(t, "name: wf\njobs:\n  j:\n    uses:\n      - use: greet\n        "+in+"\n")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}

func TestTaskMajorPinMustMatch(t *testing.T) {
	_, err := loadWithTask(t, "name: wf\nuses:\n  - use: greet@1\n    inputs: { who: x }\n")
	if err == nil || !strings.Contains(err.Error(), `pins major 1 but task "greet" is version "2.1"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestInputsOnATaskThatDeclaresNoneAreRefused(t *testing.T) {
	dir, tasks := t.TempDir(), t.TempDir()
	write(t, filepath.Join(tasks, "plain.yaml"), "name: plain\nsteps:\n  - id: a\n    run: echo hi\n")
	write(t, filepath.Join(dir, "wf.yaml"), "name: wf\nuses:\n  - use: plain\n    inputs: { x: 1 }\n")
	_, err := LoadDirWithTasks(dir, tasks, catalog())
	if err == nil || !strings.Contains(err.Error(), "declares none") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadInputDeclarationIsALoadError(t *testing.T) {
	dir, tasks := t.TempDir(), t.TempDir()
	write(t, filepath.Join(tasks, "bad.yaml"), "name: bad\ninputs:\n  n: { type: integer, default: hi }\nsteps:\n  - id: a\n    run: echo\n")
	_, err := LoadDirWithTasks(dir, tasks, catalog())
	if err == nil || !strings.Contains(err.Error(), `input "n" default`) {
		t.Fatalf("err = %v", err)
	}
}

func TestACalledWorkflowTakesInputsAndRunsAloneWithDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "callee.yaml"), `
name: callee
on: workflow_call
version: 1
inputs:
  out: { type: string, default: ctx.json }
steps:
  - id: gather
    run: echo > {{ .Inputs.out }}
`)
	write(t, filepath.Join(dir, "caller.yaml"), `
name: caller
uses:
  - use: callee@v1
    as: c
    inputs: { out: mine.json }
`)
	defs, err := LoadDirWithTasks(dir, "", catalog())
	if err != nil {
		t.Fatal(err)
	}
	if got := filled(t, defs["caller"].Steps[0].Run, nil); got != "echo > mine.json" {
		t.Fatalf("caller run = %q", got)
	}
	if got := filled(t, defs["callee"].Steps[0].Run, nil); got != "echo > ctx.json" {
		t.Fatalf("stand-alone run = %q", got)
	}
}
