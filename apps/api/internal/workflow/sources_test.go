package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSourceFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wf(name string) string {
	return "name: " + name + "\ndescription: d\ninput_schema: { type: object }\nsteps:\n" +
		"  - id: s\n    run: echo hi\n    output_schema:\n      type: object\n      properties: { ok: { type: boolean } }\n"
}

// A repository carries its workflows, the same arrangement as .github/workflows.
func TestARepositorySuppliesItsOwnWorkflows(t *testing.T) {
	repo := t.TempDir()
	writeSourceFile(t, filepath.Join(repo, ".wfnexus", "workflows"), "audit", wf("audit"))
	local := t.TempDir()
	writeSourceFile(t, local, "house", wf("house"))

	defs, skips, err := LoadSources([]Source{
		{Name: "local", Dir: local},
		RepoSource("demo", repo, ""),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %+v", skips)
	}
	// Reachable by the short name AND fully qualified, and both are the same
	// definition rather than two copies.
	short, ok := defs["audit"]
	if !ok {
		t.Fatal("the repository's workflow is not reachable by its short name")
	}
	if defs["demo/audit"] != short {
		t.Fatal("the qualified name is a different object")
	}
	if short.Source != "demo" || short.RepoDir != repo {
		t.Fatalf("source not recorded: source=%q repo=%q", short.Source, short.RepoDir)
	}
	// A listing shows each workflow ONCE, although the map holds two keys for
	// it. Iterating the map is how every listing showed everything doubled.
	if got := len(Sorted(defs)); got != 2 {
		t.Fatalf("Sorted returned %d, want 2 (house, audit)", got)
	}
}

// Importing a repository must never change what an existing name does.
func TestACollidingNameKeepsTheIncumbent(t *testing.T) {
	local := t.TempDir()
	writeSourceFile(t, local, "checks", wf("checks"))
	repo := t.TempDir()
	writeSourceFile(t, filepath.Join(repo, ".wfnexus", "workflows"), "checks", wf("checks"))

	defs, skips, err := LoadSources([]Source{
		{Name: "local", Dir: local},
		RepoSource("demo", repo, ""),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if defs["checks"].Source != "local" {
		t.Fatalf("the incumbent lost its name to %q", defs["checks"].Source)
	}
	// Both stay reachable, fully qualified.
	if defs["local/checks"] == nil || defs["demo/checks"] == nil {
		t.Fatal("a colliding workflow became unreachable")
	}
	if defs["demo/checks"] == defs["local/checks"] {
		t.Fatal("the two sources' workflows were conflated")
	}
	// And the collision is REPORTED rather than silent.
	if len(skips) != 1 {
		t.Fatalf("skips = %+v, want the collision reported", skips)
	}
}

// A repository with no workflows is normal, and a broken one must not stop the
// platform booting — importing a repository would otherwise be a way to take
// the server down.
func TestABrokenSourceIsSkippedNotFatal(t *testing.T) {
	good := t.TempDir()
	writeSourceFile(t, good, "fine", wf("fine"))
	broken := t.TempDir()
	writeSourceFile(t, filepath.Join(broken, ".wfnexus", "workflows"), "bad", "name: [this is not a workflow")
	empty := t.TempDir()

	defs, skips, err := LoadSources([]Source{
		{Name: "local", Dir: good},
		RepoSource("broken", broken, ""),
		RepoSource("empty", empty, ""),
	}, nil)
	if err != nil {
		t.Fatalf("a broken source made the whole load fail: %v", err)
	}
	if defs["fine"] == nil {
		t.Fatal("the good source did not load")
	}
	if len(skips) != 1 || skips[0].Source != "broken" {
		t.Fatalf("skips = %+v, want exactly the broken source", skips)
	}
}
