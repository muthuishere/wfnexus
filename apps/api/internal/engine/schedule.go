package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// runDAG executes a workflow whose steps declare `needs`: every step whose
// dependencies are satisfied runs CONCURRENTLY, bounded by max_parallel.
//
// This is the throughput path. A five-step pipeline where reproduce, lint and
// dependency-audit are independent finishes in the time of the slowest, not the
// sum — which is the whole point at organisation scale.
//
// It deliberately does NOT support skip_to: a forward jump has no meaning once
// steps run concurrently, and that is refused at load time rather than
// half-honoured here.
func (e *Engine) runDAG(ctx context.Context, runID uuid.UUID, def *workflow.Definition, input map[string]any, workdir, baseRef string) error {
	var mu sync.Mutex
	outputs := map[string]any{}
	done := map[string]bool{}
	started := map[string]bool{}

	// Replay: a resumed run keeps what already finished.
	existing, err := e.store.ListSteps(ctx, runID)
	if err != nil {
		return err
	}
	for _, st := range existing {
		if st.Status == "done" || st.Status == "skipped" {
			done[st.StepID] = true
			if len(st.Output) > 0 {
				var o any
				_ = json.Unmarshal(st.Output, &o)
				outputs[st.StepID] = o
			}
		}
	}

	limit := def.MaxParallel
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)

	// stop carries the first terminal outcome; the rest of the wave is allowed
	// to finish rather than being killed mid-tool-call.
	var stopOnce sync.Once
	var stopErr error
	var halted bool
	stop := func(h bool, err error) {
		stopOnce.Do(func() { halted, stopErr = h, err })
	}

	for {
		mu.Lock()
		ready := def.Ready(done, started)
		remaining := len(def.Steps) - len(done)
		mu.Unlock()

		if remaining == 0 {
			break
		}
		if halted {
			break
		}
		if len(ready) == 0 {
			// nothing runnable and nothing finished: either everything left is
			// blocked behind a halted step, or the graph stalled.
			mu.Lock()
			anyRunning := len(started) > len(done)
			mu.Unlock()
			if !anyRunning {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}

		var wave sync.WaitGroup
		for _, s := range ready {
			step := s
			mu.Lock()
			started[step.ID] = true
			snapshot := make(map[string]any, len(outputs))
			for k, v := range outputs {
				snapshot[k] = v
			}
			mu.Unlock()

			wave.Add(1)
			go func() {
				defer wave.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				data := workflow.TemplateData{
					RunID: runID.String(), WorkDir: workdir, BaseRef: baseRef,
					Input: input, Steps: snapshot,
				}
				out, outcome := e.runOneStep(ctx, runID, def, step, data)
				mu.Lock()
				defer mu.Unlock()
				switch outcome {
				case stepDone:
					outputs[step.ID] = out
					done[step.ID] = true
				case stepSkipped:
					done[step.ID] = true
				default:
					stop(true, nil)
				}
			}()
		}
		wave.Wait()
	}

	if stopErr != nil {
		return stopErr
	}
	if halted {
		return nil // the step already set the run's terminal status
	}
	e.setRun(ctx, runID, "done", "", "")
	return nil
}

type stepOutcome int

const (
	stepDone stepOutcome = iota
	stepSkipped
	stepHalted // failed, needs_input or awaiting_approval — the run stopped
)

// runOneStep executes a single step with its approval gate, judge pass, retry
// policy and gates, and records the terminal run status itself when it halts.
// Shared by the sequential and the DAG paths so the two cannot drift.
func (e *Engine) runOneStep(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData) (map[string]any, stepOutcome) {
	st, err := e.store.GetStep(ctx, runID, step.ID)
	if err != nil {
		e.failStep(ctx, runID, step.ID, err)
		return nil, stepHalted
	}
	if step.RequiresApproval && st.Status != "approved" {
		e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("awaiting_approval")})
		e.setRun(ctx, runID, "awaiting_approval", step.ID, "")
		return nil, stepHalted
	}
	e.setRun(ctx, runID, "running", step.ID, "")

	rec, vals, err := e.decide(ctx, runID, step, data)
	if err != nil {
		e.failStep(ctx, runID, step.ID, err)
		return nil, stepHalted
	}
	if rec != nil {
		e.setStep(ctx, runID, step.ID, store.StepPatch{Decision: mustJSON(rec)})
		data.Decide = vals
		if halted := e.applyDecideGatesLinear(ctx, runID, step, vals, data); halted {
			return nil, stepHalted
		}
	}

	res, err := e.executeWithRetry(ctx, runID, def, step, data)
	if err != nil {
		e.failStep(ctx, runID, step.ID, err)
		return nil, stepHalted
	}
	if res.Pending != nil {
		e.setStep(ctx, runID, step.ID, store.StepPatch{
			Status: str("needs_input"), Pending: mustJSON(res.Pending), Error: str(res.Pending.Prompt),
		})
		e.setRun(ctx, runID, "needs_input", step.ID, res.Pending.Prompt)
		return nil, stepHalted
	}
	e.setStep(ctx, runID, step.ID, store.StepPatch{
		Status: str("done"), Output: mustJSON(res.Output), FinishedAt: now(),
	})

	for _, g := range step.Gates {
		if !gateHit(g, res.Output) {
			continue
		}
		data.Output = res.Output
		msg, _ := workflow.Render(g.Message, data)
		switch g.Action {
		case "needs_input":
			e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("needs_input"), Error: str(msg)})
			e.setRun(ctx, runID, "needs_input", step.ID, msg)
			return nil, stepHalted
		case "fail":
			e.setRun(ctx, runID, "failed", step.ID, msg)
			return nil, stepHalted
		}
	}
	return res.Output, stepDone
}

// executeWithRetry re-runs a whole failed step according to its retry policy.
// This is coarser than the completion gate, which retries the MODEL inside one
// step: this retries the step's tools and workspace effects too, which is why a
// step declaring retry must be idempotent in effect.
func (e *Engine) executeWithRetry(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData) (stepResult, error) {
	attempts := 1
	if step.Retry != nil && step.Retry.MaxAttempts > attempts {
		attempts = step.Retry.MaxAttempts
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err := e.executeStep(ctx, runID, def, step, data)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if attempt == attempts || ctx.Err() != nil {
			break
		}
		backoff := time.Duration(0)
		if step.Retry != nil && step.Retry.BackoffSec > 0 {
			backoff = time.Duration(step.Retry.BackoffSec*attempt) * time.Second
		}
		e.emit(ctx, runID, step.ID, "log", map[string]any{
			"text": fmt.Sprintf("attempt %d/%d failed (%s) — retrying in %s",
				attempt, attempts, scrub(err.Error()), backoff),
		})
		e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("retrying"), Error: str(scrub(err.Error()))})
		select {
		case <-ctx.Done():
			return stepResult{}, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return stepResult{}, lastErr
}

func (e *Engine) failStep(ctx context.Context, runID uuid.UUID, stepID string, err error) {
	msg := scrub(err.Error())
	e.setStep(ctx, runID, stepID, store.StepPatch{Status: str("failed"), Error: str(msg), FinishedAt: now()})
	e.setRun(ctx, runID, "failed", stepID, msg)
}

// applyDecideGatesLinear handles the judge gates that have meaning in both
// execution modes (skip_to is refused at load time for a DAG).
func (e *Engine) applyDecideGatesLinear(ctx context.Context, runID uuid.UUID, step *workflow.Step, vals map[string]any, data workflow.TemplateData) bool {
	if step.Decide == nil {
		return false
	}
	for _, g := range step.Decide.Gates {
		if !decideGate(g, vals) {
			continue
		}
		msg, _ := workflow.Render(g.Message, data)
		switch g.Action {
		case "needs_input":
			e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("needs_input"), Error: str(msg)})
			e.setRun(ctx, runID, "needs_input", step.ID, msg)
			return true
		case "fail":
			e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("failed"), Error: str(msg), FinishedAt: now()})
			e.setRun(ctx, runID, "failed", step.ID, msg)
			return true
		}
	}
	return false
}
