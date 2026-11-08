// Package supervisor keeps service-module processes running (ADR 0028, #103).
//
// It is the same code on the server (for the labels it serves itself) and in
// wfx-runner (for a worker's labels). Each tick it asks what should run here,
// starts what is missing, stops what is no longer wanted (or was handed to
// another machine), restarts a crashed process with exponential backoff, and
// reports one heartbeat per service. The platform owns the decision of WHERE a
// service runs; this package only does what it is told and says how it went.
package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

// ErrNotOwner is what Beat returns when the platform has given the service to
// another instance. The local process is stopped: exactly one runs.
var ErrNotOwner = errors.New("another instance owns this service")

// Spec is one service to keep running here.
type Spec struct {
	Key     string            `json:"key"` // <module>/<account>
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
	Dir     string            `json:"dir,omitempty"`
}

func (s Spec) hash() string {
	b, _ := json.Marshal(s)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// Beat is one service's report.
type Beat struct {
	Key       string    `json:"key"`
	Status    string    `json:"status"` // starting | running | crashed
	PID       int       `json:"pid,omitempty"`
	Restarts  int       `json:"restarts"`
	LastError string    `json:"lastError,omitempty"`
	StartedAt time.Time `json:"startedAt,omitempty"`
}

// Supervisor is configured by its two calls to the platform.
type Supervisor struct {
	Fetch func(ctx context.Context) ([]Spec, error)
	Beat  func(ctx context.Context, b Beat) error
	// Every is the reconcile + heartbeat period (default 15s).
	Every time.Duration
	// Backoff is the first restart delay, doubled per crash up to MaxBackoff;
	// a process that stayed up for StableAfter resets it.
	Backoff, MaxBackoff, StableAfter time.Duration
	Logf                             func(format string, args ...any)

	mu   sync.Mutex
	inst map[string]*instance
}

type instance struct {
	hash   string
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	beat   Beat
}

func (s *Supervisor) defaults() {
	if s.Every <= 0 {
		s.Every = 15 * time.Second
	}
	if s.Backoff <= 0 {
		s.Backoff = time.Second
	}
	if s.MaxBackoff <= 0 {
		s.MaxBackoff = 5 * time.Minute
	}
	if s.StableAfter <= 0 {
		s.StableAfter = time.Minute
	}
	if s.Logf == nil {
		s.Logf = log.Printf
	}
}

// Stop ends every process this supervisor started.
func (s *Supervisor) Stop() { s.stopAll() }

// Run reconciles until ctx ends, then stops every process it started.
func (s *Supervisor) Run(ctx context.Context) {
	s.defaults()
	t := time.NewTicker(s.Every)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			s.stopAll()
			return
		case <-t.C:
		}
	}
}

// Tick is one reconcile and one round of heartbeats.
func (s *Supervisor) Tick(ctx context.Context) {
	s.defaults()
	specs, err := s.Fetch(ctx)
	if err != nil {
		// The platform being away is not a reason to kill what runs: keep
		// everything as it is and report on the next tick.
		s.Logf("supervisor: fetch: %v", err)
	} else {
		s.reconcile(ctx, specs)
	}
	s.mu.Lock()
	keys := make([]string, 0, len(s.inst))
	for k := range s.inst {
		keys = append(keys, k)
	}
	s.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		s.mu.Lock()
		in := s.inst[k]
		s.mu.Unlock()
		if in == nil {
			continue
		}
		in.mu.Lock()
		b := in.beat
		in.mu.Unlock()
		if err := s.Beat(ctx, b); errors.Is(err, ErrNotOwner) {
			s.Logf("supervisor: %s is owned elsewhere — stopping it here", k)
			s.stop(k)
		} else if err != nil {
			s.Logf("supervisor: heartbeat %s: %v", k, err)
		}
	}
}

func (s *Supervisor) reconcile(ctx context.Context, specs []Spec) {
	want := map[string]Spec{}
	for _, sp := range specs {
		want[sp.Key] = sp
	}
	s.mu.Lock()
	if s.inst == nil {
		s.inst = map[string]*instance{}
	}
	var gone []string
	for k, in := range s.inst {
		if sp, ok := want[k]; !ok || sp.hash() != in.hash {
			gone = append(gone, k) // removed, or changed: restart with the new spec
		}
	}
	s.mu.Unlock()
	for _, k := range gone {
		s.stop(k)
	}
	for k, sp := range want {
		s.mu.Lock()
		_, running := s.inst[k]
		if !running {
			ictx, cancel := context.WithCancel(ctx)
			in := &instance{hash: sp.hash(), cancel: cancel, done: make(chan struct{}),
				beat: Beat{Key: k, Status: "starting"}}
			s.inst[k] = in
			go s.keep(ictx, sp, in)
		}
		s.mu.Unlock()
	}
}

func (s *Supervisor) stop(k string) {
	s.mu.Lock()
	in := s.inst[k]
	delete(s.inst, k)
	s.mu.Unlock()
	if in != nil {
		in.cancel()
		<-in.done
	}
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	keys := make([]string, 0, len(s.inst))
	for k := range s.inst {
		keys = append(keys, k)
	}
	s.mu.Unlock()
	for _, k := range keys {
		s.stop(k)
	}
}

// keep runs one service until its context ends, restarting on every exit.
func (s *Supervisor) keep(ctx context.Context, sp Spec, in *instance) {
	defer close(in.done)
	backoff := s.Backoff
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, sp.Command, sp.Args...)
		cmd.Dir = sp.Dir
		cmd.Env = os.Environ()
		for k, v := range sp.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		// Ask first, so a service can release its lock and flush; kill after.
		cmd.Cancel = func() error {
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
		cmd.WaitDelay = 10 * time.Second
		started := time.Now()
		err := cmd.Start()
		if err == nil {
			in.mu.Lock()
			in.beat.Status, in.beat.PID, in.beat.StartedAt = "running", cmd.Process.Pid, started.UTC()
			in.mu.Unlock()
			err = cmd.Wait()
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("exited")
		}
		if time.Since(started) >= s.StableAfter {
			backoff = s.Backoff
		}
		in.mu.Lock()
		in.beat.Status, in.beat.PID, in.beat.LastError = "crashed", 0, err.Error()
		in.beat.Restarts++
		in.mu.Unlock()
		s.Logf("supervisor: %s %v — restarting in %s", sp.Key, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > s.MaxBackoff {
			backoff = s.MaxBackoff
		}
	}
}

// Snapshot is every service's current report (for tests and status output).
func (s *Supervisor) Snapshot() map[string]Beat {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Beat{}
	for k, in := range s.inst {
		in.mu.Lock()
		out[k] = in.beat
		in.mu.Unlock()
	}
	return out
}
