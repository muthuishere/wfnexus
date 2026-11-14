package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAModuleInstalledFromARegistrySourceIsCallableAndReportsWhatItNeeds(t *testing.T) {
	repo := newSkillRepo(t)
	dir := filepath.Join(repo.work, "modules", "greet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte(`
name: greet
version: 1.0.0
inputs: { who: { type: string, required: true } }
requires: { env: [GREET_HOST], secrets: [GREET_TOKEN] }
steps:
  - id: say
    run: echo hi {{ .Inputs.who }}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.commit("module")
	e := skillSourceEngine(t)
	if err := os.WriteFile(filepath.Join(e.cfg.WorkflowsDir, "wf.yaml"), []byte(
		"name: wf\nsteps:\n  - id: g\n    uses: greet@1\n    with: { who: ops }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GREET_HOST", "x")

	rep, err := e.ImportSkillSource(context.Background(), SkillSourceSpec{Name: "mods", URL: repo.bare, Ref: "v0", Path: "."})
	if err == nil {
		t.Fatalf("unknown ref should fail, got %+v", rep)
	}
	rep, err = e.ImportSkillSource(context.Background(), SkillSourceSpec{Name: "mods", URL: repo.bare, Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Source.Modules, []string{"greet@1.0.0"}) {
		t.Fatalf("source view modules = %v", rep.Source.Modules)
	}
	d := e.Definitions()["wf"]
	if d == nil || d.Steps[0].Run != `echo hi {{ "ops" }}` {
		t.Fatalf("wf = %+v (skips %+v)", d, e.sourceSkips)
	}
	views, errs := e.ModuleViews(context.Background())
	if len(errs) != 0 || len(views) != 1 || views[0].Ready || !reflect.DeepEqual(views[0].Missing, []string{"GREET_TOKEN"}) {
		t.Fatalf("views = %+v errs = %v", views, errs)
	}
}
