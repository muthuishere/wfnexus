package api

import (
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// A run whose workflow file is gone must still read. Every spike run in the
// store is in exactly that state, and the endpoint used to answer
// `"definition": null`, which blanked the run page.
func TestDefinitionIsReconstructedWhenTheFileIsGone(t *testing.T) {
	run := &store.Run{ID: uuid.New(), Workflow: "vanished", Project: "local"}
	steps := []*store.StepRun{
		{StepID: "gather", Position: 0},
		{StepID: "publish", Position: 1},
	}
	def := definitionFromSteps(run, steps)
	if def == nil {
		t.Fatal("no definition")
	}
	if def.Name != "vanished" {
		t.Fatalf("name: %q", def.Name)
	}
	if len(def.Steps) != 2 || def.Steps[0].ID != "gather" || def.Steps[1].ID != "publish" {
		t.Fatalf("steps not reconstructed in order: %+v", def.Steps)
	}
	// `path` stays empty: that is how a caller tells a reconstruction from a
	// definition that was actually loaded off disk.
	if def.Path != "" {
		t.Fatalf("a reconstruction must not claim a path: %q", def.Path)
	}
}
