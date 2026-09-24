package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// REUSE CARRIES EVERYTHING.
//
// A workflow that ships a run.js is not reusable if the copy brings only the
// YAML: the copy loads, and the first run fails on a file that was never
// written. These pin the whole journey — the source lists its files, the copy
// writes them, and the copy LOADS BACK with them.

const sidecarTemplateYAML = `name: shipper
template:
  title: Ships a script
  summary: a template whose step runs a file beside it
description: a template that carries a script
input_schema: { type: object }
steps:
  - id: run-it
    run: node run.js
`

const sidecarWorkflowYAML = `name: plain-shipper
description: an ordinary workflow that carries a script
input_schema: { type: object }
steps:
  - id: run-it
    run: node run.js
`

// copyEngine builds a store-free engine: copying validates and writes, and
// touches no database.
func copyEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	local := filepath.Join(root, "workflows")
	gallery := filepath.Join(root, "templates")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDirWorkflow(t, gallery, "shipper", sidecarTemplateYAML)

	cfg := config.Config{
		WorkflowsDir: local,
		TemplatesDir: gallery,
		WorkDir:      filepath.Join(root, "work"),
		SkillsDir:    filepath.Join(root, "skills"),
	}
	cat, _ := catalog.Load("", "")
	eng := New(cfg, nil, nil, map[string]*workflow.Definition{}, skills.Load(cfg.SkillsDir), cat)
	if err := eng.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	return eng, local
}

// writeDirWorkflow lays down a directory-form workflow with two sidecars.
func writeDirWorkflow(t *testing.T, dir, name, yaml string) {
	t.Helper()
	wf := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(wf, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{
		workflow.DefinitionFile: yaml,
		"run.js":                "console.log(require('./lib/greet.js').hello())\n",
		"lib/greet.js":          "module.exports.hello = () => 'from the sidecar'\n",
		"README.md":             "# not a file the platform understands, and it travels anyway\n",
	} {
		if err := os.WriteFile(filepath.Join(wf, filepath.FromSlash(p)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCopyingATemplateCarriesItsFiles(t *testing.T) {
	eng, local := copyEngine(t)

	src := eng.Definitions()["shipper"]
	if src == nil {
		t.Fatal("the template did not load")
	}
	if len(src.Files) != 3 {
		t.Fatalf("the template should carry run.js, lib/greet.js and README.md; got %v", src.Files)
	}

	def, err := eng.CopyTemplate(context.Background(), "shipper", "my-shipper")
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Files) != 3 {
		t.Fatalf("the copy dropped the files before it was even written: %v", def.Files)
	}
	if _, err := eng.SaveWorkflow(def); err != nil {
		t.Fatal(err)
	}

	// On disk, where a run will look for them.
	for _, rel := range []string{"run.js", "lib/greet.js", "README.md"} {
		p := filepath.Join(local, "my-shipper", filepath.FromSlash(rel))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the copy is missing %s: %v", rel, err)
		}
	}

	// And it LOADS BACK with them — bytes on disk that the loader does not
	// return are not a working copy.
	back := eng.Definitions()["my-shipper"]
	if back == nil {
		t.Fatal("the copy does not load")
	}
	if len(back.Files) != 3 {
		t.Fatalf("the copy loaded back without its files: %v", back.Files)
	}
	if back.Template.Is {
		t.Error("the copy is still marked a template, so it would refuse to run")
	}
	body := string(fileBody(t, back.Files, "lib/greet.js"))
	if body == "" {
		t.Error("lib/greet.js came back empty")
	}
}

// Reuse is not a template-only privilege: an ordinary workflow in another
// repository is the commonest thing anyone wants to copy.
func TestCopyingAnOrdinaryWorkflowFromAnotherSource(t *testing.T) {
	eng, local := copyEngine(t)

	other := t.TempDir()
	writeDirWorkflow(t, filepath.Join(other, ".wfx", "workflows"), "plain-shipper", sidecarWorkflowYAML)
	if _, err := eng.ImportRepo(context.Background(), "theirs", other, ""); err != nil {
		t.Fatal(err)
	}
	if eng.Definitions()["theirs/plain-shipper"] == nil {
		t.Fatal("the imported workflow did not load")
	}

	def, err := eng.CopyWorkflow(context.Background(), "theirs/plain-shipper", "ours")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SaveWorkflow(def); err != nil {
		t.Fatal(err)
	}
	back := eng.Definitions()["ours"]
	if back == nil {
		t.Fatal("the copy does not load")
	}
	if len(back.Files) != 3 {
		t.Fatalf("the copy from another repository lost its files: %v", back.Files)
	}
	if back.Source != "local" {
		t.Errorf("the copy belongs to %q, not to the person who made it", back.Source)
	}
	if _, err := os.Stat(filepath.Join(local, "ours", "run.js")); err != nil {
		t.Errorf("run.js did not come along: %v", err)
	}
}

// The listing is the whole workflow. Nothing decides which files matter.
func TestListingCarriesEveryFileWithItsSize(t *testing.T) {
	eng, _ := copyEngine(t)

	var tpl *Template
	for _, x := range eng.Templates() {
		if x.Name == "shipper" {
			tpl = &x
		}
	}
	if tpl == nil {
		t.Fatal("the template is not in the gallery")
	}
	if len(tpl.Files) != 3 {
		t.Fatalf("the gallery hides the template's files: %v", tpl.Files)
	}
	for _, f := range tpl.Files {
		if f.Size == 0 {
			t.Errorf("%s is listed with no size", f.Path)
		}
	}
	// A README is not a file the platform understands, and that is the point.
	if fileInfo(tpl.Files, "README.md") == nil {
		t.Error("the README was filtered out of the listing")
	}
}

func fileInfo(files []workflow.FileInfo, path string) *workflow.FileInfo {
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}

func fileBody(t *testing.T, files []workflow.File, path string) []byte {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f.Body
		}
	}
	t.Fatalf("no file %q in %v", path, files)
	return nil
}
