package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const callableBuild = `
name: build
on: workflow_call
env:
  GOFLAGS: -mod=mod
steps:
  - id: compile
    run: go build ./...
  - id: test
    needs: [compile]
    run: go test ./...
`

func TestAWorkflowUsesAnotherWorkflowAndRunsItsSteps(t *testing.T) {
	dir := t.TempDir()
	// The caller sorts BEFORE the callee, so file order cannot be what makes it work.
	write(t, filepath.Join(dir, "a-release.yaml"), `
name: release
uses:
  - use: build
steps:
  - id: ship
    needs: [build.test]
    run: echo ship
`)
	write(t, filepath.Join(dir, "build.yaml"), callableBuild)

	defs, err := LoadDirWithTasks(dir, "", catalog())
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(stepIDs(defs["release"]), ",")
	if got != "build.compile,build.test,ship" {
		t.Fatalf("steps = %s", got)
	}
	test := defs["release"].Steps[1]
	if len(test.Needs) != 1 || test.Needs[0] != "build.compile" {
		t.Fatalf("the callee's needs must be prefixed, got %v", test.Needs)
	}
	if test.Env["GOFLAGS"] != "-mod=mod" {
		t.Fatalf("the callee's workflow env must reach its steps, got %v", test.Env)
	}
	if got := strings.Join(stepIDs(defs["build"]), ","); got != "compile,test" {
		t.Fatalf("the callee itself must be untouched, got %s", got)
	}
}

func TestAJobUsesAWorkflowThatIsItselfBuiltFromJobs(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ci.yaml"), `
name: ci
on: [workflow_dispatch, workflow_call]
jobs:
  lint:
    steps:
      - id: vet
        run: go vet ./...
`)
	write(t, filepath.Join(dir, "nightly.yaml"), `
name: nightly
jobs:
  check:
    uses:
      - use: ci
        as: ci
`)
	defs, err := LoadDirWithTasks(dir, "", catalog())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stepIDs(defs["nightly"]), ","); got != "check.ci.lint.vet" {
		t.Fatalf("steps = %s", got)
	}
}

func TestUsingAWorkflowRequiresWorkflowCall(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "build.yaml"), "name: build\nsteps:\n  - id: b\n    run: echo b\n")
	write(t, filepath.Join(dir, "release.yaml"), "name: release\nuses:\n  - use: build\n")
	_, err := LoadDirWithTasks(dir, "", catalog())
	if err == nil || !strings.Contains(err.Error(), "workflow_call") {
		t.Fatalf("want a workflow_call refusal, got %v", err)
	}
}

func TestWorkflowsUsingEachOtherIsNamedAsACycle(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.yaml"), "name: a\non: workflow_call\nuses:\n  - use: b\n")
	write(t, filepath.Join(dir, "b.yaml"), "name: b\non: workflow_call\nuses:\n  - use: a\n")
	_, err := LoadDirWithTasks(dir, "", catalog())
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want a cycle error, got %v", err)
	}
}

func TestATaskAndAWorkflowWithTheSameNameIsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	wfs, tasks := filepath.Join(dir, "wf"), filepath.Join(dir, "tasks")
	mkdir(t, wfs)
	mkdir(t, tasks)
	write(t, filepath.Join(tasks, "build.yaml"), "name: build\nsteps:\n  - id: b\n    run: echo b\n")
	write(t, filepath.Join(wfs, "build.yaml"), callableBuild)
	write(t, filepath.Join(wfs, "release.yaml"), "name: release\nuses:\n  - use: build\n")
	_, err := LoadDirWithTasks(wfs, tasks, catalog())
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want an ambiguity error, got %v", err)
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func twoSources(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	mkdir(t, a)
	mkdir(t, b)
	return a, b
}

func TestAWorkflowUsesAWorkflowFromAnotherSource(t *testing.T) {
	shared, app := twoSources(t)
	write(t, filepath.Join(shared, "build.yaml"), callableBuild)
	write(t, filepath.Join(app, "release.yaml"), `
name: release
uses:
  - use: build
  - use: shared/build
    as: again
`)
	// The caller's source comes FIRST, so the callee is not yet loaded when it is read.
	defs, skips, err := LoadSources([]Source{{Name: "app", Dir: app}, {Name: "shared", Dir: shared}}, catalog())
	if err != nil || len(skips) != 0 {
		t.Fatalf("err=%v skips=%+v", err, skips)
	}
	got := strings.Join(stepIDs(defs["release"]), ",")
	if got != "build.compile,build.test,again.compile,again.test" {
		t.Fatalf("steps = %s", got)
	}
}

func TestAnUnknownSourceInAQualifiedUseIsASkip(t *testing.T) {
	_, app := twoSources(t)
	write(t, filepath.Join(app, "release.yaml"), "name: release\nuses:\n  - use: nowhere/build\n")
	defs, skips, err := LoadSources([]Source{{Name: "app", Dir: app}}, catalog())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defs["release"]; ok || len(skips) != 1 || !strings.Contains(skips[0].Reason, "nowhere/build") {
		t.Fatalf("want release skipped naming nowhere/build, got defs=%v skips=%+v", defs, skips)
	}
}

func TestUsingAWorkflowFromASourceThatFailedSkipsTheCaller(t *testing.T) {
	shared, app := twoSources(t)
	write(t, filepath.Join(shared, "build.yaml"), callableBuild)
	// A second, broken workflow makes the whole shared source fail validation.
	write(t, filepath.Join(shared, "broken.yaml"), "name: broken\nsteps:\n  - id: x\n    skills: [no-such-skill]\n    prompt: p\n")
	write(t, filepath.Join(app, "release.yaml"), "name: release\nuses:\n  - use: build\n")
	defs, skips, err := LoadSources([]Source{{Name: "shared", Dir: shared}, {Name: "app", Dir: app}}, catalog())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defs["release"]; ok {
		t.Fatal("a caller must not run steps from a source the platform reports as broken")
	}
	if len(skips) != 2 {
		t.Fatalf("want both sources skipped, got %+v", skips)
	}
}
