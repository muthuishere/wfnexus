package engine

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/store/pgtest"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// W4 done-when: two server instances on one Postgres fire a schedule once.
// Each instance is its own Store (its own pool) and its own Engine, exactly
// as two processes would be; both tick the same minute at the same moment.
func TestTwoInstancesFireAScheduleOnce(t *testing.T) {
	dsn, err := pgtest.DSN()
	if err != nil {
		t.Skipf("%v", err)
	}
	if err := store.Migrate("postgres", dsn); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	def := &workflow.Definition{
		Name:  "nightly",
		On:    workflow.Triggers{Schedule: []workflow.Schedule{{Cron: "* * * * *"}}},
		Steps: []workflow.Step{{ID: "a", Run: "true"}},
	}
	minute := time.Now().UTC().Truncate(time.Minute)

	var stores []*store.Store
	var engines []*Engine
	var leaders []*store.Leader
	for i := 0; i < 2; i++ {
		st, err := store.Open(ctx, "postgres", dsn)
		if err != nil {
			t.Skipf("no postgres: %v", err)
		}
		t.Cleanup(st.Close)
		cfg := config.Config{WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(), MaxConcurrentRuns: 1,
			SecretKeyEnv: "WFX_LEADER_TEST_NO_KEY", SecretKeyPath: filepath.Join(t.TempDir(), "k")}
		e := New(cfg, st, nil, map[string]*workflow.Definition{"nightly": def}, skills.Load(), nil)
		l := st.NewLeader(store.SchedulerLockKey)
		t.Cleanup(l.Release)
		e.UseLeader(l.Lead)
		stores, engines, leaders = append(stores, st), append(engines, e), append(leaders, l)
	}

	countRuns := func() int {
		runs, err := stores[0].FindRuns(ctx, store.RunFilter{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return len(runs)
	}

	var wg sync.WaitGroup
	for _, e := range engines {
		wg.Add(1)
		go func(e *Engine) { defer wg.Done(); e.scheduleTick(ctx, minute) }(e)
	}
	wg.Wait()
	if n := countRuns(); n != 1 {
		t.Fatalf("two instances started %d runs for one tick, want 1", n)
	}

	// The leader goes away (its lock released, its process gone): the other
	// instance takes over on its next tick, and still only one fires.
	for _, l := range leaders {
		l.Release()
	}
	engines[0].UseLeader(func(context.Context) bool { return false })
	next := minute.Add(time.Minute)
	for _, e := range engines {
		e.scheduleTick(ctx, next)
	}
	if n := countRuns(); n != 2 {
		t.Fatalf("after hand-over: %d runs, want 2", n)
	}
	for _, e := range engines {
		e.mu.Lock()
		for _, cancel := range e.running {
			cancel()
		}
		e.mu.Unlock()
	}
}

// SQLite is one process: its leader is always this instance.
func TestSQLiteLeaderIsAlwaysHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, b := st.NewLeader(store.SchedulerLockKey), st.NewLeader(store.SchedulerLockKey)
	if !a.Lead(context.Background()) || !b.Lead(context.Background()) {
		t.Fatal("sqlite leader not held")
	}
}
