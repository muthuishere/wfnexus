package workflow

import "testing"

// The repository's own modules must load: apl, with its whatsapp service and
// whatsapp-send command.
func TestRepoModulesLoad(t *testing.T) {
	mods, errs := FindModules("repo", "../../../../modules")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(mods) == 0 || mods[0].Name != "apl" || mods[0].Services["whatsapp"].Command != "wfx-wa-monitor" {
		t.Fatalf("mods = %+v", mods)
	}
	if _, ok := mods[0].Callable("apl.whatsapp-send"); !ok {
		t.Fatalf("commands = %v", mods[0].CommandNames())
	}
}
