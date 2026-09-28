package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// `default` takes the FALLBACK first: {{ default "main" .Input.base_branch }}.
// Written the other way round it always returns the fallback, silently
// ignoring the input. The workflow-author skill's own example had it wrong,
// an agent copied it into a real workflow, and the `since` input was ignored
// (found in the recorded demo, 2026-09-28). Nothing shipped may teach it again.
func TestNoShippedWorkflowPutsDefaultsFallbackSecond(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	wrong := regexp.MustCompile(`default\s+\.(Input|Inputs|Steps|Workflow|Env)\b`)
	for _, dir := range []string{"skills", "templates", "workflows"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if ext := filepath.Ext(p); ext != ".yaml" && ext != ".yml" && ext != ".md" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for i, line := range strings.Split(string(b), "\n") {
				if wrong.MatchString(line) {
					t.Errorf("%s:%d puts default's fallback second: %s", p, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
	}
}
