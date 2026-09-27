package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// Editing a repository's workflow must change THAT repository's file. Every
// save used to land in the platform's own directory, so the builder, opened on
// a project's workflow, wrote a second copy into `local` and left the
// repository exactly as it was.
func TestSavingIntoAProjectWritesTheProjectsFile(t *testing.T) {
	eng, local := copyEngine(t)
	repo := t.TempDir()
	wfDir := filepath.Join(repo, ".wfx", "workflows")
	writeDirWorkflow(t, wfDir, "plain-shipper", sidecarWorkflowYAML)
	if _, err := eng.ImportRepo(context.Background(), "theirs", repo, ""); err != nil {
		t.Fatal(err)
	}

	// An existing workflow, saved with no project named: back where it lives.
	def := *eng.Definitions()["theirs/plain-shipper"]
	def.Description = "edited in the builder"
	if _, err := eng.SaveWorkflowIn("", &def); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(local, "plain-shipper")); err == nil {
		t.Fatal("the edit landed in the platform's directory as a second copy")
	}
	if got := eng.Definitions()["theirs/plain-shipper"].Description; got != "edited in the builder" {
		t.Fatalf("the repository's workflow did not change: %q", got)
	}

	// A new workflow, created from the project's page: in that project.
	fresh := def
	fresh.Name, fresh.Files = "brand-new", nil
	path, err := eng.SaveWorkflowIn("theirs", &fresh)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != wfDir {
		t.Fatalf("a workflow created in project theirs was written to %s", path)
	}
	if eng.ProjectFor("brand-new") != "theirs" {
		t.Fatalf("the new workflow belongs to %q", eng.ProjectFor("brand-new"))
	}

	// A project that does not exist is refused, not quietly swapped for local.
	if _, err := eng.SaveWorkflowIn("nobody", &fresh); err == nil {
		t.Fatal("a save into a project that does not exist was accepted")
	}
}

// STARTING a project, as opposed to adding one that already has workflows.
// The result is an empty project the builder can save into — and a template
// copied from inside it lands in it, not in the platform's directory.
func TestCreatingAProjectFromNothing(t *testing.T) {
	eng, local := copyEngine(t)
	ctx := context.Background()

	src, err := eng.CreateProject(ctx, "fresh", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src.Repo, ".git")); err != nil {
		t.Errorf("a new project is not a git repository: %v", err)
	}
	if _, err := os.Stat(src.Dir); err != nil {
		t.Fatalf("a new project has no .wfx/workflows/: %v", err)
	}
	listed := false
	for _, s := range eng.Sources() {
		listed = listed || s.Name == "fresh"
	}
	if !listed {
		t.Fatal("the new project is not registered")
	}

	// The template lands IN the project.
	def, err := eng.CopyTemplate(ctx, "shipper", "first")
	if err != nil {
		t.Fatal(err)
	}
	path, err := eng.SaveWorkflowIn("fresh", def)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, src.Dir) {
		t.Fatalf("the copy went to %s, not into the project", path)
	}
	if _, err := os.Stat(filepath.Join(local, "first")); err == nil {
		t.Fatal("the copy also landed in the platform's directory")
	}

	// A name already taken is refused, not silently re-pointed.
	if _, err := eng.CreateProject(ctx, "fresh", ""); err == nil {
		t.Fatal("creating a second project named fresh was accepted")
	}
	// And a bad name is refused BEFORE a folder is touched.
	folder := t.TempDir()
	if _, err := eng.CreateProject(ctx, "../escape", folder); err == nil {
		t.Fatal("a name with a path in it was accepted")
	}
	if _, err := os.Stat(filepath.Join(folder, ".wfx")); err == nil {
		t.Fatal("a refused create still initialised the folder")
	}
}

// An existing checkout with no workflows yet can become a project in place.
func TestCreatingAProjectInAnExistingFolder(t *testing.T) {
	eng, _ := copyEngine(t)
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := eng.CreateProject(context.Background(), "mine", folder)
	if err != nil {
		t.Fatal(err)
	}
	if src.Repo != folder {
		t.Fatalf("the project is at %s, not the folder it was created in (%s)", src.Repo, folder)
	}
	if _, err := os.Stat(filepath.Join(folder, "main.go")); err != nil {
		t.Fatal("creating a project disturbed what was already in the folder")
	}
}
