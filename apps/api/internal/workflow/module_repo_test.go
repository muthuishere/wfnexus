package workflow

import "testing"

// The repository's own modules must load.
func TestRepoModulesLoad(t *testing.T) {
	mods, errs := FindModules("repo", "../../../../modules")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(mods) == 0 || mods[0].Name != "whatsapp-group-monitor" || mods[0].Kind != "service" {
		t.Fatalf("mods = %+v", mods)
	}
}
