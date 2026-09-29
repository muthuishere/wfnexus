package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Local runs evals IN THIS PROCESS, on the real engine, against a throwaway
// SQLite database and artifact folder.
//
// The engine is the real one — same step loop, same submit_output gate, same
// usage aggregate — so a cell means what a production run would mean. Only the
// run HISTORY is thrown away: an eval is dozens of runs per invocation, and
// they are samples, not work; putting them in the platform's run list would
// bury the runs somebody actually needs to see. Workflows, skills, registries
// and the runtime dir are the configured ones, so the workflow under test is
// the one this machine would run.
type Local struct {
	cfg     config.Config
	dir     string
	st      *store.Store
	timeout time.Duration

	mu      sync.Mutex
	engines map[string]*engine.Engine
}

// NewLocal prepares the throwaway store. timeout bounds each run; zero means
// thirty minutes, the order of a long agent step.
func NewLocal(cfg config.Config, timeout time.Duration) (*Local, error) {
	dir, err := os.MkdirTemp("", "wfx-eval-")
	if err != nil {
		return nil, err
	}
	cfg.StorageDriver = "sqlite"
	cfg.DatabaseURL = filepath.Join(dir, "eval.db")
	cfg.ArtifactDriver = "folder"
	cfg.ArtifactDir = filepath.Join(dir, "artifacts")
	if err := store.Migrate(cfg.StorageDriver, cfg.DatabaseURL); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("eval store: %w", err)
	}
	st, err := store.Open(context.Background(), cfg.StorageDriver, cfg.DatabaseURL)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("eval store: %w", err)
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &Local{cfg: cfg, dir: dir, st: st, timeout: timeout, engines: map[string]*engine.Engine{}}, nil
}

// Close drops the throwaway store.
func (l *Local) Close() {
	l.st.Close()
	os.RemoveAll(l.dir)
}

// engineFor builds one engine per provider, with every agent step pinned to
// it (engine.UseProvider). The workflow definition is never touched.
func (l *Local) engineFor(provider string) (*engine.Engine, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.engines[provider]; e != nil {
		return e, nil
	}
	bl, err := blob.OpenFolder(l.cfg.ArtifactDir)
	if err != nil {
		return nil, err
	}
	e := engine.New(l.cfg, l.st, bl, nil, nil, nil)
	if err := e.ReloadDefinitions(); err != nil {
		return nil, err
	}
	if _, err := e.Catalog().Providers.Require(provider); err != nil {
		return nil, err
	}
	e.UseProvider(provider)
	l.engines[provider] = e
	return e, nil
}

// Execute runs the workflow once and waits for it to stop. A run that parks
// on a human (awaiting_approval, needs_input) has stopped: the eval never
// approves or answers for anyone.
func (l *Local) Execute(ctx context.Context, provider, wf string, input map[string]any) (*Execution, error) {
	e, err := l.engineFor(provider)
	if err != nil {
		return nil, err
	}
	if e.Definitions()[wf] == nil {
		return nil, fmt.Errorf("no workflow %q on this machine", wf)
	}
	if input == nil {
		input = map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	run, err := l.st.CreateRun(ctx, e.ProjectFor(wf), wf, raw)
	if err != nil {
		return nil, err
	}
	e.Start(run.ID)
	status, errMsg, err := l.wait(ctx, e, run.ID)
	if err != nil {
		return nil, err
	}
	steps, err := l.st.ListSteps(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	ex := &Execution{Status: status, Error: errMsg,
		Outputs: map[string]json.RawMessage{}, Usage: map[string]json.RawMessage{}}
	for _, s := range steps {
		if s.Status == "done" {
			ex.Outputs[s.StepID] = s.Output
		}
		ex.Usage[s.StepID] = s.Usage
	}
	return ex, nil
}

func (l *Local) wait(ctx context.Context, e *engine.Engine, id uuid.UUID) (string, string, error) {
	deadline := time.Now().Add(l.timeout)
	for {
		r, err := l.st.GetRun(ctx, id)
		if err != nil {
			return "", "", err
		}
		switch r.Status {
		case "done", "failed", "cancelled", "awaiting_approval", "needs_input":
			return r.Status, r.Error, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			e.Cancel(context.Background(), id)
			return "timeout", fmt.Sprintf("run did not stop within %s", l.timeout), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}
