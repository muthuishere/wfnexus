package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the checkout this package lives in (apps/api/cmd/wfx -> root).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The skills that drive a person through install -> build -> deploy -> run are
// shipped: `wfx install --list` names them with their trigger line, and the
// image copies the whole skills/ folder, so the container carries them too.
func TestTheLifecycleSkillsAreShipped(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "skills")
	names, err := localSkillNames(src)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, want := range shippedLifecycleSkills {
		if !have[want] {
			t.Errorf("skills/%s is not shipped (wfx install --list would not show it)", want)
			continue
		}
		desc := skillDescription(filepath.Join(src, want, "SKILL.md"))
		if !strings.Contains(desc, "Trigger") {
			t.Errorf("skills/%s has no trigger line in its description: %q", want, desc)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "infra", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"COPY skills /app/skills", "COPY templates /app/templates", "WFX_SKILLS_DIR=/app/skills"} {
		if !strings.Contains(string(dockerfile), line) {
			t.Errorf("infra/Dockerfile does not ship them: missing %q", line)
		}
	}
}

// shippedLifecycleSkills is the demo path, one skill per stage.
var shippedLifecycleSkills = []string{"wfnexus-setup", "workflow-author", "approval-desk"}
