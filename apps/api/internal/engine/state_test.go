package engine

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// THE POINT OF THE WHOLE FEATURE, as one test: a workflow that increments a
// counter sees its own previous value on the NEXT run. Everything else here is
// a property of the scopes.

// The store is a SHARED postgres in these tests, so the workflow gets a unique
// name per test: state outlives a run by design, and "counter" would otherwise
// carry yesterday's value into today's assertion.
func counterHarness(t *testing.T) (*harness, string) {
	t.Helper()
	name := "counter-" + uuid.NewString()[:8]
	h := newHarness(t, &workflow.Definition{
		Name: name,
		Steps: []workflow.Step{{
			ID: "bump",
			// `default` takes the fallback first. An unwritten key renders
			// empty, so the first run counts from zero.
			Run: `printf %s "$(( {{ default "0" .Workflow.counter }} + 1 ))"`,
			OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"stdout": map[string]any{"type": "string"},
			}},
			State: map[string]map[string]string{
				"workflow": {"counter": "{{ .Output.stdout }}"},
				"step":     {"last_seen": "{{ .Output.stdout }}"},
			},
		}},
	}, newFakeLLM(t), "")
	t.Cleanup(func() {
		ctx := context.Background()
		_ = h.eng.DeleteState(ctx, model.StateScopeWorkflow, name, "counter")
		_ = h.eng.DeleteState(ctx, model.StateScopeStep, name+"/bump", "last_seen")
		for _, sc := range []struct{ scope, n string }{
			{model.StateScopeStep, name + "/bump"}, {model.StateScopeWorkflow, name},
			{model.StateScopeProject, "local"}, {model.StateScopeGlobal, ""},
		} {
			_ = h.eng.DeleteState(ctx, sc.scope, sc.n, "k")
		}
		_ = h.eng.DeleteState(ctx, model.StateScopeWorkflow, name, "alone")
	})
	return h, name
}

func TestStateSurvivesARun(t *testing.T) {
	h, name := counterHarness(t)
	ctx := context.Background()

	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("first run: %s — %s", run.Status, run.Error)
	}
	v, ok, err := h.eng.GetState(ctx, model.StateScopeWorkflow, name, "counter")
	if err != nil || !ok || v != "1" {
		t.Fatalf("after one run the counter is %q (ok=%v, err=%v), want 1", v, ok, err)
	}

	// A SECOND run of the same workflow reads what the first one left.
	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("second run: %s — %s", run.Status, run.Error)
	}
	if v, _, _ := h.eng.GetState(ctx, model.StateScopeWorkflow, name, "counter"); v != "2" {
		t.Fatalf("the second run did not see the first run's value: counter=%q", v)
	}

	// The same step also wrote its own scope, under its own address.
	if v, _, _ := h.eng.GetState(ctx, model.StateScopeStep, name+"/bump", "last_seen"); v != "2" {
		t.Fatalf("step state: %q", v)
	}
	// ...which is not the workflow's, and not another step's.
	if _, ok, _ := h.eng.GetState(ctx, model.StateScopeWorkflow, name, "last_seen"); ok {
		t.Fatal("step state leaked into the workflow scope")
	}
	if _, ok, _ := h.eng.GetState(ctx, model.StateScopeStep, name+"/other", "last_seen"); ok {
		t.Fatal("one step's state is visible to another")
	}
}

// The four namespaces as a STEP sees them: four values for one key, and no
// scope falling back to another.
func TestStateDataIsFourNamespaces(t *testing.T) {
	h, name := counterHarness(t)
	ctx := context.Background()
	for scope, addr := range map[string]string{
		model.StateScopeStep:     name + "/bump",
		model.StateScopeWorkflow: name,
		model.StateScopeProject:  "local",
		model.StateScopeGlobal:   "",
	} {
		if err := h.eng.SetState(ctx, scope, addr, "k", "v-"+scope); err != nil {
			t.Fatal(err)
		}
	}
	// Only the workflow scope holds `alone`.
	if err := h.eng.SetState(ctx, model.StateScopeWorkflow, name, "alone", "yes"); err != nil {
		t.Fatal(err)
	}

	step, wf, proj, global := h.eng.stateData(ctx, name, "local", "bump")
	got := map[string]map[string]string{"step": step, "workflow": wf, "project": proj, "global": global}
	for scope, m := range got {
		if m["k"] != "v-"+scope {
			t.Fatalf("%s namespace holds k=%q", scope, m["k"])
		}
		if scope != "workflow" && m["alone"] != "" {
			t.Fatalf("%s namespace fell back to the workflow scope", scope)
		}
	}

	// A step of the same workflow with a different id gets a different step
	// namespace — and the same workflow, project and global ones.
	otherStep, otherWf, _, _ := h.eng.stateData(ctx, name, "local", "other")
	if otherStep["k"] != "" {
		t.Fatalf("step `other` can see step `bump`'s state: %v", otherStep)
	}
	if otherWf["k"] != "v-workflow" {
		t.Fatal("two steps of one workflow must share the workflow scope")
	}

	// And another project cannot read this one's.
	_, _, otherProj, _ := h.eng.stateData(ctx, name, "somewhere-else", "bump")
	if otherProj["k"] != "" {
		t.Fatalf("project state crossed repositories: %v", otherProj)
	}
}

// A worker engine has no database by design. It must render empty rather than
// panic — a `runs-on:` step's prompt is rendered on the SERVER, which does have
// one, so this is the belt-and-braces case.
func TestStateOnAnEngineWithNoStore(t *testing.T) {
	e := &Engine{}
	step, wf, proj, global := e.stateData(context.Background(), "w", "p", "s")
	for _, m := range []map[string]string{step, wf, proj, global} {
		if m == nil || len(m) != 0 {
			t.Fatalf("want an empty non-nil map, got %v", m)
		}
	}
	if err := e.SetState(context.Background(), model.StateScopeGlobal, "", "k", "v"); err != ErrNoStateStore {
		t.Fatalf("a storeless engine must refuse a write, got %v", err)
	}
}

func TestStateScopeAddressing(t *testing.T) {
	if _, err := stateNameFor("nonsense", "w", "p", "s"); err == nil {
		t.Fatal("an unknown scope must be refused, not silently written somewhere")
	}
	if _, err := stateNameFor(model.StateScopeProject, "w", "", "s"); err == nil {
		t.Fatal("project state with no project must be refused")
	}
	if n, err := stateNameFor(model.StateScopeStep, "wf", "p", "scan"); err != nil || n != "wf/scan" {
		t.Fatalf("step address %q %v — two workflows may both have a step called scan", n, err)
	}
	if n, err := stateNameFor(model.StateScopeGlobal, "wf", "p", "scan"); err != nil || n != "" {
		t.Fatalf("global has one address: %q %v", n, err)
	}
}
