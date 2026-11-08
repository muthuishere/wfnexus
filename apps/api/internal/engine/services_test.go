package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/store/pgtest"
	"github.com/muthuishere/wfnexus/apps/api/internal/supervisor"
)

// serviceEngine is a skill-source engine with a real (per-run pgtest) store
// and one service module installed from a registry source.
func serviceEngine(t *testing.T) *Engine {
	t.Helper()
	dsn, err := pgtest.DSN()
	if err != nil {
		t.Skipf("%v", err)
	}
	if err := store.Migrate("postgres", dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := store.Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Skipf("no postgres: %v", err)
	}
	t.Cleanup(st.Close)
	// Each test owns its service rows; the database is shared by the package.
	t.Cleanup(func() {
		vars, _ := st.ListStateByPrefix(context.Background(), serviceScope, "")
		for _, v := range vars {
			_ = st.DeleteState(context.Background(), v.Scope, v.ScopeName, v.Key)
		}
	})

	repo := newSkillRepo(t)
	dir := filepath.Join(repo.work, "modules", "napper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte(`
name: napper
version: 0.1.0
kind: service
inputs:
  label: { type: string, default: biz }
  secs:  { type: string, required: true }
service:
  command: sleep
  args: ["{{ .Inputs.secs }}"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.commit("module")
	e := skillSourceEngine(t)
	e.store = st
	e.cfg.RunnerLabels = []string{"local"}
	if _, err := e.ImportSkillSource(context.Background(), SkillSourceSpec{Name: "mods", URL: repo.bare, Path: "."}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestAServiceRunsOnExactlyOneWorkerOfItsLabelAndGoesSilentWhenItStopsBeating(t *testing.T) {
	e := serviceEngine(t)
	ctx := context.Background()
	if _, err := e.EnableService(ctx, ServiceSpec{Module: "napper", RunsOn: "box"}); err == nil {
		t.Fatal("a missing required input must fail at start, not crash-loop on a worker")
	}
	spec, err := e.EnableService(ctx, ServiceSpec{Module: "napper", Account: "ops", RunsOn: "box", Inputs: map[string]string{"secs": "30"}})
	if err != nil || spec.Key() != "napper/ops" {
		t.Fatalf("enable = %+v, %v", spec, err)
	}

	// The server does not serve `box`, so it never takes it.
	if got, _ := e.ServicesFor(ctx, "server", e.cfg.RunnerLabels, true); len(got) != 0 {
		t.Fatalf("server took a worker's service: %+v", got)
	}
	// Two workers hold `box`: both are offered it until one beats.
	a, err := e.ServicesFor(ctx, "worker:a", []string{"box"}, false)
	if err != nil || len(a) != 1 || a[0].Command != "sleep" || !reflect.DeepEqual(a[0].Args, []string{"30"}) {
		t.Fatalf("worker a offered %+v, %v", a, err)
	}
	if err := e.ServiceBeat(ctx, "worker:a", supervisor.Beat{Key: "napper/ops", Status: "running", PID: 42}); err != nil {
		t.Fatal(err)
	}
	if b, _ := e.ServicesFor(ctx, "worker:b", []string{"box"}, false); len(b) != 0 {
		t.Fatalf("worker b offered a leased service: %+v", b)
	}
	if err := e.ServiceBeat(ctx, "worker:b", supervisor.Beat{Key: "napper/ops", Status: "running"}); !errors.Is(err, ErrServiceOwned) {
		t.Fatalf("second owner's beat = %v, want ErrServiceOwned", err)
	}
	_ = e.ServiceBeat(ctx, "worker:a", supervisor.Beat{Key: "napper/ops", Status: "crashed", Restarts: 2, LastError: "exit status 1"})

	views, _ := e.ModuleViews(ctx)
	if len(views) != 1 || len(views[0].Services) != 1 {
		t.Fatalf("views = %+v", views)
	}
	sv := views[0].Services[0]
	if sv.State != "crashed" || sv.Restarts != 2 || sv.Owner != "worker:a" || sv.LastHeartbeat == nil {
		t.Fatalf("service view = %+v", sv)
	}

	// Worker a goes quiet: silent, announced once, and b may take over.
	old := serviceSilentAfter
	serviceSilentAfter = 50 * time.Millisecond
	t.Cleanup(func() { serviceSilentAfter = old })
	time.Sleep(80 * time.Millisecond)
	if s, _ := e.Services(ctx); s[0].State != "silent" {
		t.Fatalf("state = %s, want silent", s[0].State)
	}
	if said := e.alertSilentServices(ctx); !reflect.DeepEqual(said, []string{"napper/ops"}) {
		t.Fatalf("alerted %v", said)
	}
	if said := e.alertSilentServices(ctx); len(said) != 0 {
		t.Fatalf("alerted twice: %v", said)
	}
	if b, _ := e.ServicesFor(ctx, "worker:b", []string{"box"}, false); len(b) != 1 {
		t.Fatal("a silent owner's service was not offered to another worker")
	}
	if err := e.ServiceBeat(ctx, "worker:b", supervisor.Beat{Key: "napper/ops", Status: "running"}); err != nil {
		t.Fatal(err)
	}

	// Stopped: its supervisor is refused on the next beat and drops it.
	if err := e.DisableService(ctx, "napper", "ops"); err != nil {
		t.Fatal(err)
	}
	if err := e.ServiceBeat(ctx, "worker:b", supervisor.Beat{Key: "napper/ops"}); !errors.Is(err, ErrServiceOwned) {
		t.Fatalf("beat after stop = %v", err)
	}
}

// The server's own supervisor end to end: a real process, real heartbeats.
func TestALocalServiceIsSupervisedByTheServerAndShowsRunning(t *testing.T) {
	e := serviceEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := e.EnableService(ctx, ServiceSpec{Module: "napper", Inputs: map[string]string{"secs": "30"}}); err != nil {
		t.Fatal(err)
	}
	sup := &supervisor.Supervisor{
		Fetch: func(ctx context.Context) ([]supervisor.Spec, error) {
			return e.ServicesFor(ctx, "server", e.cfg.RunnerLabels, true)
		},
		Beat: func(ctx context.Context, b supervisor.Beat) error { return e.ServiceBeat(ctx, "server", b) },
		Logf: t.Logf,
	}
	defer sup.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for {
		sup.Tick(ctx)
		s, _ := e.Services(ctx)
		if len(s) == 1 && s[0].State == "running" && s[0].Key == "napper/biz" && s[0].Owner == "server" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("services = %+v", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
