package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// Workflow memory on both databases: newest first, filtered by project and
// workflow, and forgettable per workflow.
func TestMemoryStore(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		wf := "test-mem-" + uuid.NewString()[:8]
		other := wf + "-other"
		t.Cleanup(func() {
			_, _ = st.ForgetMemory(ctx, "", wf, true)
			_, _ = st.ForgetMemory(ctx, "", other, true)
		})
		base := time.Now()
		for i, body := range []string{"oldest", "middle", "newest"} {
			if _, err := st.AddMemory(ctx, model.MemoryEntry{
				Project: "p1", Workflow: wf, Kind: model.MemoryKindRun, RunID: uuid.NewString(),
				Status: "done", Body: body, CreatedAt: base.Add(time.Duration(i) * time.Microsecond),
			}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.AddMemory(ctx, model.MemoryEntry{Project: "p2", Workflow: wf, Kind: model.MemoryKindNote, Body: "p2 lesson"}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddMemory(ctx, model.MemoryEntry{Project: "p1", Workflow: other, Kind: model.MemoryKindRun, Body: "sibling"}); err != nil {
			t.Fatal(err)
		}

		got, err := st.ListMemory(ctx, model.MemoryFilter{Project: "p1", Workflow: wf, Kind: model.MemoryKindRun, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Body != "newest" || got[1].Body != "middle" {
			t.Fatalf("newest-first with limit: %+v", got)
		}
		all, _ := st.ListMemory(ctx, model.MemoryFilter{AnyProject: true, Workflow: wf})
		if len(all) != 4 {
			t.Fatalf("any project: %d entries, want 4", len(all))
		}
		sib, _ := st.ListMemory(ctx, model.MemoryFilter{Project: "p1", NotWorkflow: wf})
		found := false
		for _, m := range sib {
			if m.Workflow == wf {
				t.Fatal("NotWorkflow leaked the excluded workflow")
			}
			found = found || m.Body == "sibling"
		}
		if !found {
			t.Fatal("NotWorkflow lost the sibling")
		}

		if err := st.DeleteMemory(ctx, got[0].ID); err != nil {
			t.Fatal(err)
		}
		n, err := st.ForgetMemory(ctx, "p1", wf, false)
		if err != nil || n != 2 {
			t.Fatalf("forget p1: n=%d err=%v, want 2", n, err)
		}
		rest, _ := st.ListMemory(ctx, model.MemoryFilter{AnyProject: true, Workflow: wf})
		if len(rest) != 1 || rest[0].Project != "p2" {
			t.Fatalf("forgetting one project touched another: %+v", rest)
		}
	})
}

// Editing an entry rewrites its text in place, keeps its order, and a missing
// id is ErrNotFound — on both databases.
func TestMemoryStoreEdit(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		wf := "test-mem-edit-" + uuid.NewString()[:8]
		t.Cleanup(func() { _, _ = st.ForgetMemory(ctx, "", wf, true) })
		base := time.Now()
		first, err := st.AddMemory(ctx, model.MemoryEntry{Project: "p", Workflow: wf, Kind: model.MemoryKindNote, Body: "old text", CreatedAt: base})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddMemory(ctx, model.MemoryEntry{Project: "p", Workflow: wf, Kind: model.MemoryKindNote, Body: "later", CreatedAt: base.Add(time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateMemoryBody(ctx, first.ID, "new text"); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetMemory(ctx, first.ID)
		if err != nil || got.Body != "new text" || got.Kind != model.MemoryKindNote || got.Workflow != wf {
			t.Fatalf("after edit: %+v err=%v", got, err)
		}
		list, _ := st.ListMemory(ctx, model.MemoryFilter{Project: "p", Workflow: wf})
		if len(list) != 2 || list[0].Body != "later" || list[1].Body != "new text" {
			t.Fatalf("edit reordered memory: %+v", list)
		}
		if err := st.UpdateMemoryBody(ctx, uuid.NewString(), "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("edit of a missing id: %v, want ErrNotFound", err)
		}
		if _, err := st.GetMemory(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get of a missing id: %v, want ErrNotFound", err)
		}
	})
}
