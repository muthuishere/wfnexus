package engine

import (
	"encoding/json"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A schedule with no `input:` sends JSON null. That used to become a nil map,
// and filling a declared default into it panicked — killing the server every
// time the schedule fired.
func TestAScheduleWithNoInputGetsItsDefaults(t *testing.T) {
	def := &workflow.Definition{
		Name: "watch",
		On:   workflow.Triggers{Schedule: []workflow.Schedule{{Cron: "*/15 * * * *"}}},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"window_min": map[string]any{"type": "integer", "default": 20},
		}},
	}
	e := &Engine{defs: map[string]*workflow.Definition{"watch": def, "proj/watch": def}}
	raw, err := e.PrepareRun("watch", workflow.TriggerSchedule, mustJSON(map[string]any(nil)))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["window_min"] != float64(20) {
		t.Fatalf("default not applied: %s", raw)
	}
}
