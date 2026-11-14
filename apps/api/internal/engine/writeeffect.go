package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A write command's approval shows what will run, and holds it to that.
//
// At the pause, every step of the call is rendered with the run's data as it
// stands — the command text with its inputs filled in — and shown to the
// person asked (scrubbed of the run's secret values). A pin over that render
// is stored with the pause. When the approved step starts, the call is
// rendered again; if it differs (the workflow was edited and reloaded, or a
// value it reads changed), the step fails instead of running something that
// was not the thing approved.

type writePause struct {
	Kind   string `json:"kind"`
	Effect string `json:"effect"`
	Pin    string `json:"pin"`
}

// writeEffect renders the write call that step opens and pins it.
func (e *Engine) writeEffect(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData) (shown, pin string) {
	prefix := ""
	if i := strings.LastIndex(step.ID, "."); i >= 0 {
		prefix = step.ID[:i+1]
	}
	// The same view of the run's data at the pause and at the start: state
	// included, as the step itself renders it.
	data = e.withState(ctx, data, def, runID, step.ID)
	var b strings.Builder
	for _, s := range def.Steps {
		if !s.WriteEffect || !strings.HasPrefix(s.ID, prefix) || s.Run == "" {
			continue
		}
		text, err := workflow.Render(s.Run, data)
		if err != nil {
			text = "(could not render: " + err.Error() + ")"
		}
		fmt.Fprintf(&b, "# %s\n%s\n", s.ID, strings.TrimRight(text, "\n"))
	}
	sum := sha256.Sum256([]byte(b.String()))
	scrub := e.scrubber(ctx, runID, e.projectOfRun(ctx, runID))
	return scrub(b.String()), hex.EncodeToString(sum[:])
}

// pauseForWrite parks a write command's first step, showing what it will run.
func (e *Engine) pauseForWrite(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData) {
	shown, pin := e.writeEffect(ctx, runID, def, step, data)
	e.setStep(ctx, runID, step.ID, model.StepPatch{
		Status: str("awaiting_approval"), Prompt: str(shown),
		Pending: mustJSON(writePause{Kind: "write", Effect: shown, Pin: pin}),
	})
	e.setRun(ctx, runID, "awaiting_approval", step.ID, "")
	msg := "step " + step.ID + " is a write and needs approval before it runs. It will run:\n" + shown
	if len(msg) > 3000 {
		msg = msg[:3000] + "\n… (the rest is on the run page)"
	}
	e.notifyPause(ctx, runID, step.ID, "approval", msg, nil)
}

// checkWritePin refuses to start an approved write call whose render is no
// longer the one approved.
func (e *Engine) checkWritePin(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData, pending json.RawMessage) error {
	var p writePause
	if len(pending) == 0 || json.Unmarshal(pending, &p) != nil || p.Kind != "write" || p.Pin == "" {
		return fmt.Errorf("step %s was approved without the record of what it would run; approve it again", step.ID)
	}
	if _, pin := e.writeEffect(ctx, runID, def, step, data); pin != p.Pin {
		return fmt.Errorf("what step %s would run changed after it was approved (the workflow or a value it reads changed); approve it again", step.ID)
	}
	return nil
}
