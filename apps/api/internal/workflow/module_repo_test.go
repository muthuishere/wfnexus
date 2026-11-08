package workflow

import (
	"strings"
	"testing"
)

// The repository's own modules must load: apl, gh, aos.
func TestRepoModulesLoad(t *testing.T) {
	mods, errs := FindModules("repo", "../../../../modules")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	by := map[string]*Module{}
	for _, m := range mods {
		by[m.Name] = m
	}
	if by["apl"] == nil || by["apl"].Services["whatsapp"].Command != "wfx-wa-monitor" || by["gh"] == nil || by["aos"] == nil {
		t.Fatalf("mods = %v", by)
	}
	for _, c := range []string{"apl.whatsapp-send", "apl.mail-send", "gh.pr-comment", "aos.shell"} {
		mod, _, _ := strings.Cut(c, ".")
		if _, ok := by[mod].Callable(c); !ok {
			t.Fatalf("%s missing: %v", c, by[mod].CommandNames())
		}
	}
}
