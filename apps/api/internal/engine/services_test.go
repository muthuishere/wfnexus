package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
services:
  nap:
    command: sleep
    args: ["{{ .Inputs.secs }}"]
    inputs:
      label: { type: string, default: biz }
      secs:  { type: string, required: true }
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
	if _, err := e.EnableService(ctx, ServiceSpec{Module: "napper.nap", RunsOn: "box"}); err == nil {
		t.Fatal("a missing required input must fail at start, not crash-loop on a worker")
	}
	spec, err := e.EnableService(ctx, ServiceSpec{Module: "napper.nap", Account: "ops", RunsOn: "box", Inputs: map[string]string{"secs": "30"}})
	if err != nil || spec.Key() != "napper.nap/ops" {
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
	if err := e.ServiceBeat(ctx, "worker:a", supervisor.Beat{Key: "napper.nap/ops", Status: "running", PID: 42}); err != nil {
		t.Fatal(err)
	}
	if b, _ := e.ServicesFor(ctx, "worker:b", []string{"box"}, false); len(b) != 0 {
		t.Fatalf("worker b offered a leased service: %+v", b)
	}
	if err := e.ServiceBeat(ctx, "worker:b", supervisor.Beat{Key: "napper.nap/ops", Status: "running"}); !errors.Is(err, ErrServiceOwned) {
		t.Fatalf("second owner's beat = %v, want ErrServiceOwned", err)
	}
	_ = e.ServiceBeat(ctx, "worker:a", supervisor.Beat{Key: "napper.nap/ops", Status: "crashed", Restarts: 2, LastError: "exit status 1"})

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
	if said := e.alertSilentServices(ctx); !reflect.DeepEqual(said, []string{"napper.nap/ops"}) {
		t.Fatalf("alerted %v", said)
	}
	if said := e.alertSilentServices(ctx); len(said) != 0 {
		t.Fatalf("alerted twice: %v", said)
	}
	if b, _ := e.ServicesFor(ctx, "worker:b", []string{"box"}, false); len(b) != 1 {
		t.Fatal("a silent owner's service was not offered to another worker")
	}
	if err := e.ServiceBeat(ctx, "worker:b", supervisor.Beat{Key: "napper.nap/ops", Status: "running"}); err != nil {
		t.Fatal(err)
	}

	// Stopped: its supervisor is refused on the next beat and drops it.
	if err := e.DisableService(ctx, "napper.nap", "ops"); err != nil {
		t.Fatal(err)
	}
	if err := e.ServiceBeat(ctx, "worker:b", supervisor.Beat{Key: "napper.nap/ops"}); !errors.Is(err, ErrServiceOwned) {
		t.Fatalf("beat after stop = %v", err)
	}
}

func TestWatchesAreRegisteredThroughTheAPIAndAHungServiceGoesSilent(t *testing.T) {
	e := serviceEngine(t)
	ctx := context.Background()
	if _, err := e.PutServiceWatch(ctx, "napper.nap/ops", json.RawMessage(`{"name":"w"}`)); err == nil {
		t.Fatal("a watch on a service that was never started must fail")
	}
	if _, err := e.EnableService(ctx, ServiceSpec{Module: "napper.nap", Account: "ops", RunsOn: "box", Inputs: map[string]string{"secs": "30"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutServiceWatch(ctx, "napper.nap/ops", json.RawMessage(`{"chat":"x"}`)); err == nil {
		t.Fatal("a watch without a name must fail")
	}
	for _, w := range []string{`{"name":"ops","chat":"a@g.us","hook":"h1"}`, `{"name":"all","chat":"*","hook":"h2"}`, `{"name":"ops","chat":"b@g.us","hook":"h1"}`} {
		if _, err := e.PutServiceWatch(ctx, "napper.nap/ops", json.RawMessage(w)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.DeleteServiceWatch(ctx, "napper.nap/ops", "all"); err != nil {
		t.Fatal(err)
	}
	ws, _ := e.ServiceWatches(ctx, "napper.nap/ops")
	if len(ws) != 1 || !strings.Contains(string(ws[0]), "b@g.us") {
		t.Fatalf("watches = %s", ws)
	}

	// The supervisor hands the service a token scoped to it alone.
	specs, _ := e.ServicesFor(ctx, "worker:a", []string{"box"}, false)
	if len(specs) != 1 {
		t.Fatalf("specs = %+v", specs)
	}
	tok := specs[0].Env[supervisor.EnvToken]
	if specs[0].Env[supervisor.EnvKey] != "napper.nap/ops" || e.AuthService(ctx, "napper.nap/ops", tok) != nil {
		t.Fatalf("service env = %v", specs[0].Env)
	}
	if e.AuthService(ctx, "napper.nap/other", tok) == nil || e.AuthService(ctx, "napper.nap/ops", "wfs_nope") == nil {
		t.Fatal("a service token must not open another service")
	}

	// Alive per the supervisor, but its own loop never beats: silent.
	old := serviceSilentAfter
	serviceSilentAfter = 100 * time.Millisecond
	t.Cleanup(func() { serviceSilentAfter = old })
	beat := func() {
		if err := e.ServiceBeat(ctx, "worker:a", supervisor.Beat{Key: "napper.nap/ops", Status: "running", PID: 7, StartedAt: time.Now().Add(-time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	beat()
	if s, _ := e.Services(ctx); s[0].State != "silent" || !strings.Contains(s[0].LastError, "hung") {
		t.Fatalf("hung service = %+v", s[0])
	}
	if said := e.alertSilentServices(ctx); len(said) != 1 {
		t.Fatalf("alerted %v", said)
	}
	beat()
	if said := e.alertSilentServices(ctx); len(said) != 0 {
		t.Fatalf("alerted twice while still hung: %v", said)
	}
	// It beats from inside: running again, and the alert re-arms.
	if err := e.ServiceSelfBeat(ctx, "napper.nap/ops"); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Services(ctx)
	if s[0].State != "running" || s[0].ServiceHeartbeat == nil {
		t.Fatalf("after its own beat = %+v", s[0])
	}
	e.alertSilentServices(ctx)
	time.Sleep(150 * time.Millisecond)
	beat()
	if said := e.alertSilentServices(ctx); len(said) != 1 {
		t.Fatalf("a second hang was not announced: %v", said)
	}

	// Stop drops its watches with it.
	if err := e.DisableService(ctx, "napper.nap", "ops"); err != nil {
		t.Fatal(err)
	}
	if ws, _ := e.ServiceWatches(ctx, "napper.nap/ops"); len(ws) != 0 {
		t.Fatalf("watches survived stop: %s", ws)
	}
}

// The server's own supervisor end to end: a real process, real heartbeats.
func TestALocalServiceIsSupervisedByTheServerAndShowsRunning(t *testing.T) {
	e := serviceEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := e.EnableService(ctx, ServiceSpec{Module: "napper.nap", Inputs: map[string]string{"secs": "30"}}); err != nil {
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
		if len(s) == 1 && s[0].State == "running" && s[0].Key == "napper.nap/biz" && s[0].Owner == "server" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("services = %+v", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
