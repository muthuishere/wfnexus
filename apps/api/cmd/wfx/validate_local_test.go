package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// `wfx validate` checks the shape locally with the loader's own code, so a
// `start:` step — which the loader accepts — must pass it.
func TestLocalCheckAcceptsStartSteps(t *testing.T) {
	for _, name := range []string{"frontdesk", "morning-brief"} {
		path := filepath.Join("..", "..", "..", "..", "templates", name, workflow.DefinitionFile)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		d, err := workflow.DecodeYAMLStrict(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, files, err := workflow.ReadWorkflowDir(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		d.Files = files
		if err := workflow.Check(d, localCatalog(path)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLocalCheckStillRejectsAStepWithNoKind(t *testing.T) {
	d, err := workflow.DecodeYAMLStrict([]byte("name: empty-step\nsteps:\n  - id: nothing\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Check(d, anyCatalog{}); err == nil {
		t.Fatal("a step with no prompt/run/judge/send/start passed")
	}
}
