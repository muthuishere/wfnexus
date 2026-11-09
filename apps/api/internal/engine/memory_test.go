package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The memory home is toolnexus's: the memory tool writes MEMORY.md and notes,
// the next step loads the (capped) index, notes are never auto-loaded, and the
// management API reads/writes/deletes the same files.
func TestMemoryHomeRoundTrip(t *testing.T) {
	name := uniq("mem")
	def := convDef(t, name, "echo hi")
	h := startHarness(t, def)
	ctx := context.Background()
	run := h.waitEnded(h.start(name, map[string]any{}))
	dir, err := h.eng.MemoryDir(run.Project, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	tools := h.eng.memoryTool(ctx, run.ID, name, &workflow.Step{ID: "s"})
	if len(tools) != 1 || tools[0].Name != "memory" {
		t.Fatalf("tools %+v", tools)
	}
	mem := tools[0]
	if r, _ := mem.Execute(map[string]any{"action": "add", "text": "deploy notes: see [[deploy]]"}, nil); r.IsError {
		t.Fatal(r.Output)
	}
	if r, _ := mem.Execute(map[string]any{"action": "add", "target": "note:deploy", "text": "argo-pipeline-77"}, nil); r.IsError {
		t.Fatal(r.Output)
	}
	got := h.eng.memoryFor(def, "s", run.Project)
	if !strings.Contains(got, "[[deploy]]") || strings.Contains(got, "argo-pipeline-77") {
		t.Fatalf("loaded:\n%s", got)
	}
	v, err := h.eng.WorkflowMemory(ctx, name, run.Project)
	if err != nil || len(v.Notes) != 1 || v.Notes[0].Slug != "deploy" {
		t.Fatalf("view %+v %v", v, err)
	}
	if err := h.eng.WriteMemory(ctx, name, run.Project, "note:log", "x"); err == nil {
		t.Fatal("log.md must be append-only")
	}
	if err := h.eng.DeleteMemory(name, run.Project, "note:deploy"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "memory", "deploy.md")); !os.IsNotExist(err) {
		t.Fatal("note not deleted")
	}
	if def.Memory = (&workflow.Memory{Off: true}); h.eng.memoryFor(def, "s", run.Project) != "" {
		t.Fatal("memory: false still loads")
	}
}
