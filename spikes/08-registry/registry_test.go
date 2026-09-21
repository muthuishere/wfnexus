package registry

import (
	"os"
	"path/filepath"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"
)

// The real machine registry, as our platform sees it. #93 changed both how many
// skills parse AND which copy wins a duplicate name.
func TestMachineRegistry(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".claude", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skip("no ~/.claude/skills here")
	}
	inv := tn.ListSkills(tn.LoadSkillsOptions{Dirs: []string{root}})
	t.Logf("parsed=%d skipped=%d", len(inv.Skills), len(inv.Skipped))

	reasons := map[tn.SkillSkipReason]int{}
	for _, s := range inv.Skipped {
		reasons[s.Reason]++
	}
	for r, n := range reasons {
		t.Logf("  skipped %-24s %d", r, n)
	}
	if reasons["malformed-frontmatter"] > 0 {
		t.Errorf("#93 still open: %d skills fail to parse", reasons["malformed-frontmatter"])
	}

	// which copy wins for the names that exist twice
	for _, name := range []string{"docx", "pdf", "pptx", "xlsx"} {
		for _, s := range inv.Skills {
			if s.Name == name {
				t.Logf("  %-6s -> %s", name, s.Location)
			}
		}
	}
}
