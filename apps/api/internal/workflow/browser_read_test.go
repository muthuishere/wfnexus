package workflow

import (
	"path/filepath"
	"testing"
)

// browser.act is a write (the engine pauses before it); browser.read is not,
// and loads with no approval on any of its steps.
func TestBrowserReadIsNotPausedAndActIs(t *testing.T) {
	mods, errs := FindModules("builtin", filepath.Join("..", "..", "..", "..", "modules"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "b.yaml"), `name: b
steps:
  - id: look
    uses: browser.read@0
    with: { action: open, args: "[]" }
  - id: click
    uses: browser.act@0
    with: { action: click, args: "[]" }
`)
	defs, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	var read, act int
	for _, s := range defs["b"].Steps {
		if !s.RequiresApproval {
			continue
		}
		switch s.Module {
		case "browser":
			if len(s.ID) >= 5 && s.ID[:5] == "look." {
				read++
			} else {
				act++
			}
		}
	}
	if read != 0 || act != 1 {
		t.Fatalf("approvals: read %d, act %d (want 0 and 1)", read, act)
	}
}
