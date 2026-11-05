package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A note a person adds by hand, and an edit a person makes to a lesson an agent
// wrote, are what the NEXT run's agent reads — and the replaced text is gone.
func TestHandWrittenAndEditedMemoryReachTheNextRun(t *testing.T) {
	name := memName("mem-edit")
	llm := newFakeLLM(t,
		// run 1: an agent records a lesson that turns out to be wrong
		callTool("remember", map[string]any{"lesson": "deploys go through jenkins-old-xyz"}),
		submit(map[string]any{"verdict": "one"}), finish(),
		// run 2
		submit(map[string]any{"verdict": "two"}), finish(),
	)
	h := newHarness(t, &workflow.Definition{
		Name:  name,
		Steps: []workflow.Step{{ID: "triage", Prompt: "Triage {{ .Input.ticket }}", OutputSchema: verdictSchema, MaxTurns: 6, MaxAttempts: 1}},
	}, llm, "")
	forgetAfter(t, h, name)
	ctx := context.Background()

	if run := h.run(map[string]any{"ticket": "T-1"}); run.Status != "done" {
		t.Fatalf("run 1: %s — %s", run.Status, run.Error)
	}
	notes, _ := h.store.ListMemory(ctx, model.MemoryFilter{Project: "local", Workflow: name, Kind: model.MemoryKindNote})
	if len(notes) != 1 {
		t.Fatalf("want the agent's lesson, got %+v", notes)
	}
	edited, err := h.eng.EditMemoryEntry(ctx, notes[0].ID, "deploys go through the argo-new-pipeline")
	if err != nil {
		t.Fatal(err)
	}
	if edited.StepID != "triage" || edited.Kind != model.MemoryKindNote {
		t.Fatalf("edit lost the entry's identity: %+v", edited)
	}
	if _, err := h.eng.AddMemoryNote(ctx, name, "local", "always\nCC the on-call channel-qq"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.AddMemoryNote(ctx, name, "local", "   "); err == nil {
		t.Fatal("an empty note was accepted")
	}
	if _, err := h.eng.EditMemoryEntry(ctx, notes[0].ID, ""); err == nil {
		t.Fatal("an empty edit was accepted")
	}

	if run := h.run(map[string]any{"ticket": "T-2"}); run.Status != "done" {
		t.Fatalf("run 2: %s — %s", run.Status, run.Error)
	}
	p := llm.promptOf(3) // requests 0..2 were run 1
	for _, want := range []string{"[triage] deploys go through the argo-new-pipeline", "- always CC the on-call channel-qq"} {
		if !strings.Contains(p, want) {
			t.Fatalf("run 2's prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "jenkins-old-xyz") {
		t.Fatalf("run 2 still read the replaced lesson:\n%s", p)
	}
}

// An edit of a run record keeps its lines; a missing id is an error.
func TestEditMemoryRunRecordAndMissing(t *testing.T) {
	name := memName("mem-edit-run")
	h := newHarness(t, &workflow.Definition{Name: name, Steps: []workflow.Step{{ID: "s", Prompt: "go", OutputSchema: verdictSchema, MaxTurns: 6, MaxAttempts: 1}}}, newFakeLLM(t), "")
	forgetAfter(t, h, name)
	ctx := context.Background()
	m, err := h.store.AddMemory(ctx, model.MemoryEntry{Project: "local", Workflow: name, Kind: model.MemoryKindRun, Status: "done", Body: "run x"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.eng.EditMemoryEntry(ctx, m.ID, "run x · done\noutcome: corrected by hand")
	if err != nil || got.Body != "run x · done\noutcome: corrected by hand" {
		t.Fatalf("run record edit: %q err=%v", got.Body, err)
	}
	if !strings.Contains(h.eng.PreviewMemory(ctx, h.eng.defs[name]), "outcome: corrected by hand") {
		t.Fatal("the edited record is not what the next run loads")
	}
	if _, err := h.eng.EditMemoryEntry(ctx, "no-such-id", "x"); err == nil {
		t.Fatal("editing a missing entry succeeded")
	}
}
