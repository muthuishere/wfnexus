package workflow

import "testing"

// The shipped browser module loads: read, and act marked as a write.
func TestShippedBrowserModuleLoads(t *testing.T) {
	mods, errs := FindModules("builtin", "../../../../modules")
	for _, e := range errs {
		t.Error(e)
	}
	for _, m := range mods {
		if m.Name != "browser" {
			continue
		}
		if len(m.Commands) != 2 {
			t.Fatalf("browser commands = %d, want 2", len(m.Commands))
		}
		act, okAct := m.Commands["act"]
		read, okRead := m.Commands["read"]
		if !okAct || !okRead || act.Effect != "write" || read.Effect == "write" {
			t.Fatalf("browser commands: act %v (effect %q), read %v (effect %q)", okAct, act.Effect, okRead, read.Effect)
		}
		return
	}
	t.Fatal("browser module not found")
}
