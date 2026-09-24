package engine

import (
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// THE PRECEDENCE TABLE, asserted rather than only written down.
//
//	system → project → run facts (WFX_*, WFX_MOUNT_*) → workflow → step
//
// Later wins, and only for the names it mentions. The workflow and step levels
// have already been merged into step.Env by the loader, which is what the
// loader test below pins.
func TestStepEnvPrecedence(t *testing.T) {
	runID := uuid.New()
	e := &Engine{attached: map[uuid.UUID]mountState{
		runID: {Env: map[string]string{
			"WFX_MOUNT_DATA": "/ws/data",
			// The run level is BELOW the file, so a workflow that sets this
			// name on purpose still wins.
			"OVERRIDDEN": "from the run",
		}},
	}}
	step := &workflow.Step{ID: "s", Env: map[string]string{
		"OVERRIDDEN": "from the file",
		"MINE":       "step value",
	}}

	got, err := e.stepEnv(t.Context(), runID, step, "/ws")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"WFX_MOUNT_DATA": "/ws/data", // the mount destination reaches the step
		"WFX_RUN_ID":     runID.String(),
		"WFX_WORKSPACE":  "/ws", // and so do the run's own facts
		"WFX_STEP_ID":    "s",
		"OVERRIDDEN":     "from the file", // the file is the last word
		"MINE":           "step value",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

// When a step is PACKED for another machine, the workspace names are left out
// — the workspace is that machine's, and a path from this one would be a lie
// the script would then act on. The worker fills them in itself.
func TestPackedStepGetsNoLocalWorkspacePath(t *testing.T) {
	runID := uuid.New()
	e := &Engine{attached: map[uuid.UUID]mountState{runID: {}}}
	got, err := e.stepEnv(t.Context(), runID, &workflow.Step{ID: "s"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["WFX_WORKSPACE"]; ok {
		t.Fatal("a step packed for a worker must not carry this machine's workspace path")
	}
}
