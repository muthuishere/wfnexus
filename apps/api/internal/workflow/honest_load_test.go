package workflow

import (
	"strings"
	"testing"
)

// `runs_on:` is not a field (`runs-on` is). Before, yaml dropped it and the
// workflow ran on the server instead of the workers it named.
func TestAnUnknownKeyIsALoadError(t *testing.T) {
	_, err := DecodeYAMLStrict([]byte("name: x\nruns_on: gpu\nsteps:\n  - id: s\n    run: echo\n"))
	if err == nil || !strings.Contains(err.Error(), "runs_on") {
		t.Fatalf("want an error naming runs_on, got %v", err)
	}
	// ...and a step-level typo too.
	_, err = DecodeYAMLStrict([]byte("name: x\nsteps:\n  - id: s\n    run: echo\n    continue_on_error: true\n"))
	if err == nil || !strings.Contains(err.Error(), "continue_on_error") {
		t.Fatalf("want an error naming continue_on_error, got %v", err)
	}
	d, err := DecodeYAMLStrict([]byte("name: x\nruns-on: gpu\nsteps:\n  - id: s\n    run: echo\n    continue-on-error: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.RunsOn != "gpu" || !d.Steps[0].ContinueOnError {
		t.Fatalf("known keys not read: %+v", d)
	}
}

// A file with an unknown key is skipped by name; its siblings still load.
func TestAnUnknownKeySkipsOnlyThatWorkflow(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "good", wf("good"))
	writeSourceFile(t, dir, "typo", strings.Replace(wf("typo"), "description: d\n", "description: d\nruns_on: gpu\n", 1))
	defs, skips, err := LoadSources([]Source{{Name: "p", Dir: dir}}, catalog())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defs["good"]; !ok {
		t.Fatal("the good workflow must still load")
	}
	if _, ok := defs["typo"]; ok {
		t.Fatal("a workflow with an unknown key must not load")
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "runs_on") {
		t.Fatalf("want one skip naming runs_on, got %+v", skips)
	}
}

// The reqsume incident: one workflow naming a missing skill made the whole
// project show 0 workflows, which silently stopped its schedules.
func TestAMissingSkillSkipsOnlyThatWorkflow(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "watch", wf("watch"))
	writeSourceFile(t, dir, "triage", "name: triage\nsteps:\n  - id: x\n    skills: [no-such-skill]\n    prompt: p\n    output_schema: { type: object }\n")
	defs, skips, err := LoadSources([]Source{{Name: "reqsume", Dir: dir}}, catalog())
	if err != nil {
		t.Fatal(err)
	}
	if defs["watch"] == nil || defs["reqsume/watch"] == nil {
		t.Fatalf("the healthy workflow must stay loaded; skips=%+v", skips)
	}
	if len(skips) != 1 || skips[0].Workflow != "triage" || skips[0].Source != "reqsume" || !strings.Contains(skips[0].Reason, "no-such-skill") {
		t.Fatalf("want triage skipped with its reason, got %+v", skips)
	}
}

// Two files in one project with one name: the first keeps it, the other is
// reported rather than silently overwriting it.
func TestADuplicateNameInOneSourceKeepsTheFirst(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "a", wf("same"))
	writeSourceFile(t, dir, "b", strings.Replace(wf("same"), "description: d", "description: second", 1))
	defs, skips, err := LoadSources([]Source{{Name: "p", Dir: dir}}, catalog())
	if err != nil {
		t.Fatal(err)
	}
	if defs["same"] == nil || defs["same"].Description != "d" {
		t.Fatalf("want the first file kept, got %+v", defs["same"])
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "duplicate") || !strings.HasSuffix(skips[0].Location, "b.yaml") {
		t.Fatalf("want b.yaml reported as a duplicate, got %+v", skips)
	}
}
