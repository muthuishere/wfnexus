package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dirFormYAML = `name: report
description: ships its own script
mount:
  - fixtures:data:ro
steps:
  - id: run-it
    run: node run.js
`

func writeWorkflowDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	wf := filepath.Join(root, "report")
	if err := os.MkdirAll(filepath.Join(wf, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) {
		if err := os.WriteFile(filepath.Join(wf, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(DefinitionFile, dirFormYAML)
	write("run.js", "console.log('hello from run.js')\n")
	write(filepath.Join("lib", "format.js"), "module.exports = {}\n")
	return root
}

// THE DIRECTORY IS THE DECLARATION. Nothing in the YAML lists run.js, and it
// still travels with the workflow.
func TestADirectoryWorkflowCarriesTheFilesBesideIt(t *testing.T) {
	defs, err := LoadDir(writeWorkflowDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := defs["report"]
	if d == nil {
		t.Fatal("the directory form did not load")
	}
	if len(d.Files) != 2 {
		t.Fatalf("expected run.js and lib/format.js, got %v", d.Files)
	}
	var paths []string
	for _, f := range d.Files {
		paths = append(paths, f.Path)
	}
	// Always slash-separated: these are written out on a machine whose
	// separator may not be this one's.
	if strings.Join(paths, ",") != "lib/format.js,run.js" {
		t.Fatalf("paths = %v", paths)
	}
	if len(d.Mount) != 1 || d.Mount[0].At != "data" || !d.Mount[0].ReadOnly {
		t.Fatalf("mount = %+v", d.Mount)
	}
}

// The flat form is untouched: adding the directory form must break nothing
// that already exists.
func TestAFlatWorkflowStillLoadsAndCarriesNoFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flat.yaml"),
		[]byte("name: flat\nsteps:\n  - id: s\n    run: echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defs, err := LoadDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if defs["flat"] == nil || len(defs["flat"].Files) != 0 {
		t.Fatalf("the flat form changed: %+v", defs["flat"])
	}
}

// A save must not lose the files — the whole reason they are carried in the
// definition rather than read off disk at the last moment.
func TestSaveKeepsTheFilesBesideTheWorkflow(t *testing.T) {
	defs, err := LoadDir(writeWorkflowDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	path, err := Save(out, defs["report"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != DefinitionFile {
		t.Fatalf("a workflow with files must be saved as a directory, got %s", path)
	}
	back, err := LoadDir(out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(back["report"].Files) != 2 {
		t.Fatalf("the files did not survive the save: %v", back["report"].Files)
	}
	// And deleting takes the whole thing, not just the YAML.
	if err := Delete(out, "report"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "report")); err == nil {
		t.Fatal("deleting a workflow must take its files with it")
	}
}

// A sidecar and a mount are different things, and the loader keeps them from
// colliding rather than letting a repository's file overwrite a user's folder.
func TestASidecarInsideAMountIsRefusedAtLoad(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "clash")
	if err := os.MkdirAll(filepath.Join(wf, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, DefinitionFile),
		[]byte("name: clash\nmount:\n  - fixtures:data:ro\nsteps:\n  - id: s\n    run: echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "data", "seed.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "mount") {
		t.Fatalf("a file staged inside a mount must be refused at load: %v", err)
	}
}

func TestCheckFilesRefusesAnEscape(t *testing.T) {
	for _, p := range []string{"../evil.js", "/etc/cron.d/evil", `..\evil.js`} {
		if err := CheckFiles("wf", []File{{Path: p}}, nil); err == nil {
			t.Errorf("%q must be refused", p)
		}
	}
}
