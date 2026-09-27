package engine

import (
	"context"
	"fmt"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"log"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// StartScheduler fires `on: schedule:` entries. It ticks once a minute, which
// is the resolution a five-field cron expression has — polling faster would
// only find the same answer sooner.
//
// It is deliberately the dullest possible implementation:
//
//   - A tick asks each loaded workflow whether any of its cron entries matches
//     THIS minute. No timers per entry, nothing to cancel and re-arm when a
//     workflow is edited, and a reload needs no coordination because the
//     definitions are read fresh on every tick.
//   - The minute is truncated and remembered, so a tick that arrives twice for
//     the same minute — a slow tick, a clock adjustment — starts one run, not
//     two. Actions has the same at-most-once-per-minute property.
//   - A missed minute is NOT backfilled. If the process was down at 09:00 the
//     09:00 run does not happen; it is not queued for 09:05 when nobody is
//     watching and the reason has passed. Actions does the same, and a
//     catch-up storm after a restart is worse than a skipped run.
//
// Scheduled runs are per-process. With more than one engine process this would
// start N runs per tick; that is the point at which a claim in Postgres is
// needed, and it is recorded in docs/not-now.md rather than guessed at here.
func (e *Engine) StartScheduler(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		// Fire on the tick only, never at start-up: booting at 09:00:30 must
		// not run the 09:00 schedule a second time after a deploy.
		var last time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				minute := now.UTC().Truncate(time.Minute)
				if !minute.After(last) {
					continue
				}
				last = minute
				e.fireSchedules(ctx, minute)
			}
		}
	}()
}

// fireSchedules starts a run for every cron entry matching this minute.
func (e *Engine) fireSchedules(ctx context.Context, minute time.Time) {
	// A project workflow is reachable by its short name AND as project/name —
	// two keys, one definition. Firing per KEY started every schedule twice.
	seen := map[*workflow.Definition]bool{}
	for name, def := range e.Definitions() {
		if seen[def] {
			continue
		}
		seen[def] = true
		for i, sched := range def.On.Schedule {
			c, err := workflow.ParseCron(sched.Cron)
			if err != nil {
				// Unreachable: the loader validated it. Logged rather than
				// ignored, because silence here would hide a schedule that
				// never fires.
				log.Printf("scheduler: %s schedule[%d] %q: %v", name, i, sched.Cron, err)
				continue
			}
			if !c.Matches(minute) {
				continue
			}
			if err := e.startScheduled(ctx, name, sched.Input); err != nil {
				log.Printf("scheduler: %s (%s): %v", name, sched.Cron, err)
				continue
			}
			log.Printf("scheduler: started %s on %q", name, sched.Cron)
		}
	}
}

// MarkInterrupted runs at boot. A run is executed by a goroutine of THIS
// process; when the process stops, every run it was executing stops with it —
// and used to stay "running" forever, a step spinning on a page with nothing
// behind it. At boot nothing is executing yet, so a run still marked running
// or queued, on a step this process would have run itself, was interrupted.
// It is marked failed, saying so, which makes it visible and retryable
// (`wfx retry <run> --step <step>`), rather than silently resumed with state
// nobody can vouch for.
//
// A step that belongs to a WORKER is left alone: the worker may still be
// executing it and will report back.
func (e *Engine) MarkInterrupted(ctx context.Context) int {
	if e.store == nil {
		return 0
	}
	runs, err := e.store.RunsInStatus(ctx, "running", "queued")
	if err != nil {
		log.Printf("boot: listing interrupted runs: %v", err)
		return 0
	}
	n := 0
	for _, r := range runs {
		if e.stepOnWorker(r.Workflow, r.CurrentStep) {
			continue
		}
		msg := "interrupted by a server restart — retry from this step to continue"
		if r.CurrentStep != "" {
			e.setStep(ctx, r.ID, r.CurrentStep, model.StepPatch{Status: str("failed"), Error: str(msg), FinishedAt: now()})
		}
		e.setRun(ctx, r.ID, "failed", r.CurrentStep, msg)
		n++
	}
	return n
}

// stepOnWorker reports whether a run's current step is one a worker executes.
func (e *Engine) stepOnWorker(workflowName, stepID string) bool {
	def := e.Definitions()[workflowName]
	if def == nil {
		return false
	}
	for i := range def.Steps {
		if def.Steps[i].ID == stepID {
			return !e.servesLocally(def.Steps[i].RunsOn)
		}
	}
	return false
}

// startScheduled is StartTriggered with a floor under it: one schedule that
// panics is logged as a failed start, not a dead server. The scheduler runs in
// the server's own process, so an unrecovered panic here took down every run in
// flight and the API with them.
func (e *Engine) startScheduled(ctx context.Context, name string, input map[string]any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panicked starting a scheduled run: %v", r)
		}
	}()
	return e.StartTriggered(ctx, name, workflow.TriggerSchedule, input)
}

// StartTriggered creates and starts a run attributed to a trigger. It is the
// one path for everything that is not a person pressing a button, so the
// trigger check and the input preparation cannot be skipped by a new caller.
func (e *Engine) StartTriggered(ctx context.Context, name string, trigger workflow.TriggerKind, input map[string]any) error {
	raw, err := e.PrepareRun(name, trigger, mustJSON(input))
	if err != nil {
		return err
	}
	run, err := e.store.CreateRun(ctx, e.ProjectFor(name), name, raw)
	if err != nil {
		return err
	}
	e.Start(run.ID)
	return nil
}
