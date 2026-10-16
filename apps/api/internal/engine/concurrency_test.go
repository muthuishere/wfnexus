package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func concEngine(t *testing.T, defs map[string]*workflow.Definition) (*Engine, *store.Store) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return New(config.Config{WorkDir: t.TempDir()}, st, nil, defs, skills.Load(), nil), st
}

func concDef(name string, c *workflow.Concurrency) *workflow.Definition {
	return &workflow.Definition{Name: name, Concurrency: c, Steps: []workflow.Step{{ID: "a", Run: "true"}}}
}

func admit(t *testing.T, e *Engine, name string, force bool) (string, error) {
	t.Helper()
	r, err := e.CreateRunAdmitted(context.Background(), name, json.RawMessage(`{}`), force)
	if err != nil {
		return "", err
	}
	return r.ID.String(), nil
}

func TestConcurrencySkipsWhileActiveAndStartsAfterFinish(t *testing.T) {
	e, st := concEngine(t, map[string]*workflow.Definition{"w": concDef("w", &workflow.Concurrency{})})
	first, err := admit(t, e, "w", false)
	if err != nil {
		t.Fatal(err)
	}
	// queued holds the group
	_, err = admit(t, e, "w", false)
	var busy *BusyError
	if !errors.As(err, &busy) || busy.RunID != first || !errors.Is(err, ErrConcurrencyBusy) {
		t.Fatalf("want busy on %s, got %v", first, err)
	}
	// --force goes past it
	if _, err := admit(t, e, "w", true); err != nil {
		t.Fatalf("force refused: %v", err)
	}
	// finish every run; the group is free again
	runs, _ := st.RunsInStatus(context.Background(), "queued", "running")
	for _, r := range runs {
		_ = st.UpdateRun(context.Background(), r.ID, "done", "", "")
	}
	if _, err := admit(t, e, "w", false); err != nil {
		t.Fatalf("finished run still blocks: %v", err)
	}
}

func TestConcurrencyParkedOnHumanDoesNotBlock(t *testing.T) {
	e, st := concEngine(t, map[string]*workflow.Definition{"w": concDef("w", &workflow.Concurrency{})})
	for _, status := range []string{"awaiting_approval", "needs_input"} {
		id, err := admit(t, e, "w", false)
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		r, _ := st.RunsInStatus(context.Background(), "queued")
		for _, x := range r {
			if x.ID.String() == id {
				_ = st.UpdateRun(context.Background(), x.ID, status, "a", "")
			}
		}
	}
	if _, err := admit(t, e, "w", false); err != nil {
		t.Fatalf("a parked run blocked the group: %v", err)
	}
}

func TestConcurrencyGroupSpansWorkflows(t *testing.T) {
	g := &workflow.Concurrency{Group: "db"}
	e, _ := concEngine(t, map[string]*workflow.Definition{"a": concDef("a", g), "b": concDef("b", g), "c": concDef("c", nil)})
	if _, err := admit(t, e, "a", false); err != nil {
		t.Fatal(err)
	}
	if _, err := admit(t, e, "b", false); !errors.Is(err, ErrConcurrencyBusy) {
		t.Fatalf("same group should block: %v", err)
	}
	if _, err := admit(t, e, "c", false); err != nil {
		t.Fatalf("no concurrency must not block: %v", err)
	}
}

func TestConcurrencyCancelInProgress(t *testing.T) {
	e, st := concEngine(t, map[string]*workflow.Definition{"w": concDef("w", &workflow.Concurrency{CancelInProgress: true})})
	ctx := context.Background()
	old, err := e.CreateRunAdmitted(ctx, "w", json.RawMessage(`{}`), false)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateRun(ctx, old.ID, "running", "a", "")
	if _, err := admit(t, e, "w", false); err != nil {
		t.Fatalf("cancel-in-progress refused: %v", err)
	}
	got, _ := st.GetRun(ctx, old.ID)
	if got.Status != "cancelled" {
		t.Fatalf("older run is %q, want cancelled", got.Status)
	}
}

// Two ticks at once must start exactly one run.
func TestConcurrencyRaceAdmitsExactlyOne(t *testing.T) {
	e, _ := concEngine(t, map[string]*workflow.Definition{"w": concDef("w", &workflow.Concurrency{})})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, busy := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := admit(t, e, "w", false)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrConcurrencyBusy):
				busy++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || busy != 15 {
		t.Fatalf("admitted %d, busy %d; want 1 and 15", ok, busy)
	}
}

// The scheduler path goes through the same gate.
func TestScheduledRunIsSkippedWhileActive(t *testing.T) {
	def := concDef("w", &workflow.Concurrency{})
	def.On.Schedule = []workflow.Schedule{{Cron: "* * * * *"}}
	e, _ := concEngine(t, map[string]*workflow.Definition{"w": def})
	if _, err := admit(t, e, "w", false); err != nil {
		t.Fatal(err)
	}
	err := e.startScheduled(context.Background(), "w", nil)
	if !errors.Is(err, ErrConcurrencyBusy) {
		t.Fatalf("scheduled run not skipped: %v", err)
	}
}
