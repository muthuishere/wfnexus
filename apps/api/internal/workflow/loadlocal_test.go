package workflow

import (
	"path/filepath"
	"testing"
)

// One workflow with a YAML error in the platform's own directory used to exit
// the server at boot, taking every other workflow down with it.
func TestLoadLocalSkipsABrokenWorkflowAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "good", wf("good"))
	writeSourceFile(t, dir, "bad", "name: bad\ndescription: d\nsteps:\n  - id: s\n    run: a: b: c\n")

	if _, err := LoadDir(dir, nil); err == nil {
		t.Fatal("LoadDir should stay strict (save validates with it)")
	}
	defs, skips, err := LoadLocal(dir, nil)
	if err != nil {
		t.Fatalf("one broken file must not fail the load: %v", err)
	}
	if defs["good"] == nil {
		t.Fatal("the valid workflow did not load")
	}
	if len(skips) != 1 || skips[0].Source != "local" || filepath.Base(skips[0].Location) != "bad.yaml" || skips[0].Reason == "" {
		t.Fatalf("the broken workflow is not reported: %+v", skips)
	}
	t.Logf("NOT LOADED %s: %s", skips[0].Location, skips[0].Reason)
}
