package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Each loop iteration runs under its own step id, and a step sees it as
// WFX_STEP_ID — what an idempotency key built from run and step id needs to
// tell iterations apart (conversation.start_task).
func TestALoopIterationSeesItsOwnStepID(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ids")
	def := &workflow.Definition{Name: "loop-ids", Steps: []workflow.Step{{
		ID: "each", Foreach: []any{"a", "b"},
		Run: `printf '%s\n' "$WFX_STEP_ID" >> ` + out,
	}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	b, _ := os.ReadFile(out)
	ids := strings.Fields(string(b))
	if len(ids) != 2 || ids[0] == ids[1] || !strings.Contains(ids[0], "[0]") || !strings.Contains(ids[1], "[1]") {
		t.Fatalf("step ids = %q", ids)
	}
}
