package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// activeStatuses hold a concurrency group. A run parked on a human
// (awaiting_approval, needs_input) deliberately does not: an unanswered pause
// would otherwise stop the workflow from ever running again.
var activeStatuses = []string{"running", "queued"}

// ErrConcurrencyBusy is returned when a run is refused because another run of
// its concurrency group is still active.
var ErrConcurrencyBusy = errors.New("concurrency")

// BusyError names the run holding the group.
type BusyError struct {
	Group string
	RunID string
}

func (b *BusyError) Error() string {
	return fmt.Sprintf("skipped: concurrency (run %s still active in group %q)", b.RunID, b.Group)
}
func (b *BusyError) Unwrap() error { return ErrConcurrencyBusy }

// CreateRunAdmitted creates a run of the named workflow subject to its
// `concurrency:`. The check and the insert happen under one lock, so two
// callers racing for one group get exactly one run. With force the check is
// skipped (a person said so); with cancel-in-progress the active runs of the
// group are cancelled and the new one is admitted.
//
// Per-process, like the scheduler: one engine owns the runs it starts.
func (e *Engine) CreateRunAdmitted(ctx context.Context, name string, input json.RawMessage, force bool) (*model.Run, error) {
	def := e.Definitions()[name]
	if def == nil || def.Concurrency == nil || force {
		return e.store.CreateRun(ctx, e.ProjectFor(name), name, input)
	}
	group := def.Concurrency.GroupFor(def.Name)

	e.admitMu.Lock()
	defer e.admitMu.Unlock()
	active, err := e.store.RunsInStatus(ctx, activeStatuses...)
	if err != nil {
		return nil, err
	}
	for _, r := range active {
		if e.groupOf(r.Workflow) != group {
			continue
		}
		if !def.Concurrency.CancelInProgress {
			return nil, &BusyError{Group: group, RunID: r.ID.String()}
		}
		log.Printf("concurrency: cancelling run %s (%s) for a newer run in group %q", r.ID, r.Workflow, group)
		e.Cancel(ctx, r.ID)
	}
	return e.store.CreateRun(ctx, e.ProjectFor(name), name, input)
}

// groupOf is the concurrency group of a workflow by its key; "" for none.
func (e *Engine) groupOf(name string) string {
	d := e.Definitions()[name]
	if d == nil || d.Concurrency == nil {
		return ""
	}
	return d.Concurrency.GroupFor(d.Name)
}
