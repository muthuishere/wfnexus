package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// THE `start:` STEP (ADR 0026 decision 5).
//
// A `start:` step starts other workflows as INDEPENDENT runs, each recorded
// with this run as its parent (parent_run_id, parent_step_id, parent_mode).
//
//   - nowait (default): start and move on; the output names the child.
//   - wait: true: the parent run parks in status `waiting` until every child
//     has ended; then it is started again, and the step's output is the
//     child's output (its last finished step's). A child that failed fails
//     the step, unless the step says continue-on-error.
//   - continue: event: the parent run finishes NOW. When the child ends, a
//     `workflow.completed` event carrying the parent's conversation key is
//     delivered to the event sink (the router), or — with no sink — handed
//     to the parent's workflow in that same conversation.
//
// Waiting is durable in the database, not in a goroutine: the parent's step
// is re-entered on restart, finds its children by (parent, step), and only
// starts them the first time.

// runStatusEnded are the statuses a child is finished in.
var runStatusEnded = map[string]bool{"done": true, "failed": true, "cancelled": true}

// UseEventSink wires where `workflow.completed` events go — the router. The
// sink returns true when it handled the event; false (or no sink) falls back
// to starting the parent's workflow in the parent's conversation.
func (e *Engine) UseEventSink(fn func(ctx context.Context, ev map[string]any) bool) {
	e.eventSink = fn
}

// runStartStep executes a `start:` step. It records the step's status itself.
func (e *Engine) runStartStep(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData) (map[string]any, stepOutcome) {
	fail := func(err error) (map[string]any, stepOutcome) {
		e.failStep(ctx, runID, step.ID, err)
		return nil, stepHalted
	}
	mode := step.Start.Mode()
	if mode == "event" && (def.IsDAG() || def.IsPlanned()) {
		return fail(fmt.Errorf("start: `continue: event` finishes the run, which needs a sequential workflow (no needs/consumes)"))
	}
	parent, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return fail(err)
	}
	children, err := e.childrenOfStep(ctx, runID, step.ID)
	if err != nil {
		return fail(err)
	}
	if len(children) == 0 {
		if children, err = e.startChildren(ctx, parent, def, step, mode, data); err != nil {
			return fail(err)
		}
	}

	if mode == "wait" {
		if !allEnded(children) {
			e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("waiting")})
			e.setRun(ctx, runID, "waiting", step.ID, "")
			// A child that ended between the read above and the status landing
			// saw a parent that was not yet waiting; look once more.
			if children, err = e.childrenOfStep(ctx, runID, step.ID); err != nil || !allEnded(children) {
				return nil, stepHalted
			}
			e.setRun(ctx, runID, "running", step.ID, "")
		}
	}

	out := e.startOutput(ctx, step, mode, children)
	if mode == "wait" && !step.ContinueOnError {
		for _, c := range children {
			if c.Status != "done" {
				e.setStep(ctx, runID, step.ID, model.StepPatch{Output: mustJSON(out)})
				return fail(fmt.Errorf("start: child run %s (%s) %s: %s (set continue-on-error: true to branch on it instead)", c.ID, c.Workflow, c.Status, c.Error))
			}
		}
	}
	e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("done"), Output: mustJSON(out), FinishedAt: now()})
	e.persistStepState(ctx, def, step, data, out)
	if halted := e.applyOutputGates(ctx, runID, step, out, data); halted {
		return nil, stepHalted
	}
	if mode == "event" {
		// The run is finished here; the rest comes back as an event.
		e.skipUntaken(ctx, runID, "not reached: the run continued as an event when "+step.ID+" started its workflow")
		e.setRun(ctx, runID, "done", "", "")
		return out, stepHalted
	}
	return out, stepDone
}

func allEnded(runs []*model.Run) bool {
	for _, r := range runs {
		if !runStatusEnded[r.Status] {
			return false
		}
	}
	return true
}

func (e *Engine) childrenOfStep(ctx context.Context, runID uuid.UUID, stepID string) ([]*model.Run, error) {
	all, err := e.store.ChildRuns(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out []*model.Run
	for _, r := range all {
		if r.ParentStepID == stepID {
			out = append(out, r)
		}
	}
	return out, nil
}

// runDepth is how deep a run sits: 1 for a top-level run.
func (e *Engine) runDepth(ctx context.Context, run *model.Run) int {
	depth := 1
	for r := run; r.ParentRunID != "" && depth <= workflow.MaxStartDepth+1; depth++ {
		id, err := uuid.Parse(r.ParentRunID)
		if err != nil {
			break
		}
		if r, err = e.store.GetRun(ctx, id); err != nil {
			break
		}
	}
	return depth
}

func (e *Engine) startChildren(ctx context.Context, parent *model.Run, def *workflow.Definition, step *workflow.Step, mode string, data workflow.TemplateData) ([]*model.Run, error) {
	if d := e.runDepth(ctx, parent); d >= workflow.MaxStartDepth {
		return nil, fmt.Errorf("start: this run is already %d levels deep (run → child → …); runs nest at most %d levels, so it may not start another", d, workflow.MaxStartDepth)
	}
	existing, err := e.store.ChildRuns(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	if n := len(existing) + len(step.Start.Targets); n > workflow.MaxStartFanOut {
		return nil, fmt.Errorf("start: this run would have %d child runs; one run may start at most %d", n, workflow.MaxStartFanOut)
	}
	// Render and check EVERY target before starting any, so a bad second
	// target does not leave the first one running alone.
	type planned struct {
		name  string
		input map[string]any
	}
	var plan []planned
	for _, t := range step.Start.Targets {
		name, err := workflow.Render(t.Workflow, data)
		if err != nil {
			return nil, fmt.Errorf("start: workflow: %w", err)
		}
		// A name computed by an earlier step (a router's `run:` printing it)
		// usually ends in a newline.
		name = strings.TrimSpace(name)
		if e.Definitions()[name] == nil {
			return nil, fmt.Errorf("start: workflow %q is not loaded", name)
		}
		in, err := renderAny(t.Input, data)
		if err != nil {
			return nil, fmt.Errorf("start: %s input: %w", name, err)
		}
		m, _ := in.(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		plan = append(plan, planned{name, m})
	}
	var out []*model.Run
	for _, p := range plan {
		child, err := e.startChild(ctx, parent, step.ID, mode, p.name, p.input)
		if err != nil {
			return out, fmt.Errorf("start: %s: %w", p.name, err)
		}
		e.emit(ctx, parent.ID, step.ID, "run.child", map[string]any{"runId": child.ID.String(), "workflow": p.name, "mode": mode})
		out = append(out, child)
	}
	// Re-read: a fast child may already have moved on.
	return e.childrenOfStep(ctx, parent.ID, step.ID)
}

func (e *Engine) startChild(ctx context.Context, parent *model.Run, stepID, mode, name string, input map[string]any) (*model.Run, error) {
	def := e.Definitions()[name]
	// A child is CALLED; a workflow that only declares dispatch is still
	// startable the way a person would start it.
	trig := workflow.TriggerWorkflowCall
	if !def.On.Allows(trig) {
		trig = workflow.TriggerDispatch
	}
	raw, err := e.PrepareRun(name, trig, mustJSON(input))
	if err != nil {
		return nil, err
	}
	run, err := e.CreateRunAdmitted(ctx, name, raw, false)
	if err != nil {
		return nil, err
	}
	if err := e.store.SetRunLinks(ctx, run.ID, parent.ID.String(), stepID, mode, ""); err != nil {
		return nil, err
	}
	if err := e.store.SetTriggeredBy(ctx, run.ID, "run:"+parent.ID.String()); err != nil {
		return nil, err
	}
	e.Start(run.ID)
	return run, nil
}

// jsonOnly is a value that is exactly `{{ json <path> }}`.
var jsonOnly = regexp.MustCompile(`^\s*\{\{-?\s*json\s+[^{}]+?\s*-?\}\}\s*$`)

// renderAny renders every string inside v as a template. A value that is
// exactly `{{ json <path> }}` passes that value WITH ITS TYPE — an object
// stays an object — so a router can hand an event to a workflow whose
// input_schema wants `message: {type: object}`.
func renderAny(v any, data workflow.TemplateData) (any, error) {
	switch t := v.(type) {
	case string:
		out, err := workflow.Render(t, data)
		if err != nil || !jsonOnly.MatchString(t) {
			return out, err
		}
		var typed any
		if json.Unmarshal([]byte(out), &typed) == nil {
			return typed, nil
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			r, err := renderAny(x, data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			r, err := renderAny(x, data)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

// runOutput is a finished run's output: its last finished step's output.
func (e *Engine) runOutput(ctx context.Context, runID uuid.UUID) any {
	steps, err := e.store.ListSteps(ctx, runID)
	if err != nil {
		return nil
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	for i := len(steps) - 1; i >= 0; i-- {
		st := steps[i]
		if st.Status == "done" && len(st.Output) > 0 && !isIterationRow(st.StepID) {
			var o any
			if json.Unmarshal(st.Output, &o) == nil {
				return o
			}
		}
	}
	return nil
}

func (e *Engine) childOutput(ctx context.Context, c *model.Run, withOutput bool) map[string]any {
	m := map[string]any{"runId": c.ID.String(), "workflow": c.Workflow, "status": c.Status}
	if !runStatusEnded[c.Status] {
		m["status"] = "started"
	}
	if c.Error != "" {
		m["error"] = c.Error
	}
	if withOutput && runStatusEnded[c.Status] {
		m["output"] = e.runOutput(ctx, c.ID)
	}
	return m
}

func (e *Engine) startOutput(ctx context.Context, step *workflow.Step, mode string, children []*model.Run) map[string]any {
	wait := mode == "wait"
	if !step.Start.List && len(children) == 1 {
		return e.childOutput(ctx, children[0], wait)
	}
	runs := make([]any, 0, len(children))
	status := "done"
	for _, c := range children {
		co := e.childOutput(ctx, c, wait)
		runs = append(runs, co)
		switch {
		case !wait:
			status = "started"
		case co["status"] != "done":
			status = "failed"
		}
	}
	return map[string]any{"runs": runs, "status": status}
}

// ---- when a run's status changes ----

// afterRunStatus runs after every status write. Cheap for the statuses that
// matter to nobody here (running, queued).
func (e *Engine) afterRunStatus(ctx context.Context, runID uuid.UUID, status string) {
	if e.store == nil {
		return
	}
	if !runStatusEnded[status] && !laneReleasing[status] {
		return
	}
	ctx = context.WithoutCancel(ctx)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("engine: after run %s %s: %v", runID, status, r)
		}
	}()
	e.conversationAfterStatus(ctx, runID, status)
	if !runStatusEnded[status] {
		return
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil || run.ParentRunID == "" {
		return
	}
	parentID, err := uuid.Parse(run.ParentRunID)
	if err != nil {
		return
	}
	switch run.ParentMode {
	case "wait":
		go e.wakeParent(parentID, run.ParentStepID)
	case "event":
		e.childCompleted(ctx, run, parentID, status)
	}
}

// wakeParent starts a waiting parent again once every child of its step has
// ended. The parent's goroutine may still be unwinding, so Start is retried
// for a short while until it takes.
func (e *Engine) wakeParent(parentID uuid.UUID, stepID string) {
	ctx := context.Background()
	kids, err := e.childrenOfStep(ctx, parentID, stepID)
	if err != nil || !allEnded(kids) {
		return
	}
	for i := 0; i < 100; i++ {
		p, err := e.store.GetRun(ctx, parentID)
		if err != nil || p.Status != "waiting" {
			return // not parked (yet, or any more): its own re-check covers it
		}
		e.mu.Lock()
		_, live := e.running[parentID]
		e.mu.Unlock()
		if !live {
			e.Start(parentID)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	log.Printf("engine: run %s is waiting on children that ended, but could not be restarted", parentID)
}

// childCompleted delivers the `workflow.completed` event of a child started
// with `continue: event`, once.
func (e *Engine) childCompleted(ctx context.Context, child *model.Run, parentID uuid.UUID, status string) {
	e.conv.mu.Lock()
	if e.conv.completed == nil {
		e.conv.completed = map[uuid.UUID]bool{}
	}
	if e.conv.completed[child.ID] {
		e.conv.mu.Unlock()
		return
	}
	e.conv.completed[child.ID] = true
	e.conv.mu.Unlock()

	parent, err := e.store.GetRun(ctx, parentID)
	if err != nil {
		log.Printf("engine: workflow.completed for %s: parent %s: %v", child.ID, parentID, err)
		return
	}
	ev := map[string]any{
		"type":           "workflow.completed",
		"kind":           "workflow.completed",
		"workflow":       child.Workflow,
		"runId":          child.ID.String(),
		"status":         status,
		"output":         e.runOutput(ctx, child.ID),
		"parentRunId":    parent.ID.String(),
		"parentStepId":   child.ParentStepID,
		"parentWorkflow": parent.Workflow,
		"project":        parent.Project,
	}
	if child.Error != "" {
		ev["error"] = child.Error
	}
	conv := conversationOfRun(parent)
	key, inConv := ParseConversationKey(conv)
	if inConv {
		ev["conversation"] = conv
		ev["channel"], ev["chat"] = key.Channel, key.Chat
	}
	e.emit(ctx, parent.ID, child.ParentStepID, "workflow.completed", ev)
	if e.eventSink != nil && e.eventSink(ctx, ev) {
		return
	}
	if !inConv {
		return // nobody to hand it to: it is on the parent's event log
	}
	// No router took it: the parent's own workflow handles it, in the same
	// conversation, after whatever that conversation is doing now.
	input := map[string]any{"event": ev, "conversation": map[string]any{"channel": key.Channel, "chat": key.Chat}}
	if err := e.enqueueConversation(key, parent.Workflow, input, false); err != nil {
		log.Printf("engine: workflow.completed for %s: %v", child.ID, err)
	}
}
