package supervisor

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// The test binary doubles as the service: SUPERVISOR_CHILD=crash exits 3 at
// once, =sleep stays up until killed.
func TestMain(m *testing.M) {
	switch os.Getenv("SUPERVISOR_CHILD") {
	case "crash":
		os.Exit(3)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func child(key, mode string) Spec {
	return Spec{Key: key, Command: os.Args[0], Args: []string{"-test.run=^$"}, Env: map[string]string{"SUPERVISOR_CHILD": mode}}
}

type beats struct {
	mu   sync.Mutex
	last map[string]Beat
	deny map[string]bool
}

func (b *beats) beat(_ context.Context, x Beat) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deny[x.Key] {
		return ErrNotOwner
	}
	b.last[x.Key] = x
	return nil
}

func (b *beats) get(k string) Beat {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last[k]
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestACrashingServiceIsRestartedWithBackoffAndReportsItsRestarts(t *testing.T) {
	b := &beats{last: map[string]Beat{}, deny: map[string]bool{}}
	specs := []Spec{child("m/crash", "crash"), child("m/up", "sleep")}
	s := &Supervisor{
		Fetch:   func(context.Context) ([]Spec, error) { return specs, nil },
		Beat:    b.beat,
		Backoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond,
		Logf: func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.Stop()
	waitFor(t, "3 restarts", func() bool {
		s.Tick(ctx)
		return b.get("m/crash").Restarts >= 3
	})
	if got := b.get("m/crash"); got.Status != "crashed" && got.Status != "running" || got.LastError == "" {
		t.Fatalf("crash beat = %+v", got)
	}
	waitFor(t, "the sleeper running", func() bool {
		s.Tick(ctx)
		x := b.get("m/up")
		return x.Status == "running" && x.PID > 0 && x.Restarts == 0
	})
}

func TestAServiceOwnedElsewhereOrRemovedIsStopped(t *testing.T) {
	b := &beats{last: map[string]Beat{}, deny: map[string]bool{}}
	specs := []Spec{child("m/a", "sleep"), child("m/b", "sleep")}
	var mu sync.Mutex
	s := &Supervisor{
		Fetch: func(context.Context) ([]Spec, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]Spec(nil), specs...), nil
		},
		Beat: b.beat, Logf: func(string, ...any) {},
	}
	ctx := context.Background()
	defer s.Stop()
	waitFor(t, "both running", func() bool {
		s.Tick(ctx)
		snap := s.Snapshot()
		return snap["m/a"].Status == "running" && snap["m/b"].Status == "running"
	})
	// Another machine holds m/a: the next heartbeat is refused and it stops.
	b.mu.Lock()
	b.deny["m/a"] = true
	b.mu.Unlock()
	mu.Lock()
	specs = specs[1:2] // and m/a is no longer offered here
	mu.Unlock()
	s.Tick(ctx)
	if _, ok := s.Snapshot()["m/a"]; ok {
		t.Fatal("m/a still supervised after it was owned elsewhere")
	}
	// m/b is withdrawn: stopped on the next reconcile.
	mu.Lock()
	specs = nil
	mu.Unlock()
	s.Tick(ctx)
	if len(s.Snapshot()) != 0 {
		t.Fatalf("still running: %v", s.Snapshot())
	}
}
