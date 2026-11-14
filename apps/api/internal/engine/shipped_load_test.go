package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Every shipped workflow and template loads under the strict loader (unknown
// keys refused) against the repo's own skills and registries, with nothing
// skipped. This is `wfx validate` over workflows/ and templates/, in CI.
func TestEveryShippedWorkflowAndTemplateLoads(t *testing.T) {
	reg := skills.Load(repoPath(t, "skills"))
	cat, err := catalog.Load(repoPath(t, "registries.json"), "")
	if err != nil {
		t.Fatalf("registries: %v", err)
	}
	defs, skips, err := workflow.LoadSources([]workflow.Source{
		{Name: "workflows", Dir: repoPath(t, "workflows")},
		{Name: "templates", Dir: repoPath(t, "templates")},
	}, catalog.NewValidator(reg, cat), shippedModules(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skips {
		t.Errorf("%s/%s did not load: %s", s.Source, s.Workflow, s.Reason)
	}
	if len(defs) == 0 {
		t.Fatal("nothing loaded")
	}
	if defs["conversation-task"] == nil {
		t.Error("the conversation-task template (it uses a module) did not load")
	}
	t.Logf("%d workflows loaded", len(workflow.Sorted(defs)))
}

// No shipped workflow or template names an API provider. A step either names
// no provider (it runs on the auto-detected agent CLI) or names a local agent
// CLI / ACP entry — or the in-process mock. An API key is never a default.
func TestNoShippedWorkflowNamesAnAPIProvider(t *testing.T) {
	cat, err := catalog.Load(repoPath(t, "registries.json"), "")
	if err != nil {
		t.Fatalf("registries: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*-?\s*provider:\s*["']?([A-Za-z0-9_.-]+)`)
	for _, root := range []string{"workflows", "templates"} {
		_ = filepath.WalkDir(repoPath(t, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
				return err
			}
			b, _ := os.ReadFile(path)
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				p, ok := cat.Providers.Get(m[1])
				if !ok {
					t.Errorf("%s: provider %q is not in the registry", path, m[1])
					continue
				}
				if p.Kind != catalog.KindCLI && p.Kind != catalog.KindACP && p.Kind != catalog.KindMock {
					t.Errorf("%s: provider %q is %s — shipped workflows use agent CLIs only, never an API", path, m[1], p.Kind)
				}
			}
			return nil
		})
	}
}

// shippedModules is the modules in this repository as a load option: what a
// `uses: <module>.<command>` step resolves to, exactly as on a server.
func shippedModules(t *testing.T) workflow.LoadOption {
	t.Helper()
	mods, errs := workflow.FindModules("builtin", repoPath(t, "modules"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return workflow.WithModules(mods)
}
