package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// `on:` is a CONTROL, not a note. This project's recurring failure is a field
// that expresses intent and is then ignored — `workdir`, `team`, `provider`
// — so the declaration is enforced where a run is created, and there is exactly
// one path that does it.
func TestATriggerThatIsNotDeclaredIsRefused(t *testing.T) {
	scheduleOnly := &workflow.Definition{
		Name: "nightly",
		On:   workflow.Triggers{Schedule: []workflow.Schedule{{Cron: "0 2 * * *"}}},
	}
	callOnly := &workflow.Definition{Name: "reusable", On: workflow.Triggers{Call: true}}
	dispatchOnly := &workflow.Definition{Name: "manual", On: workflow.Triggers{Dispatch: true}}
	e := &Engine{defs: map[string]*workflow.Definition{
		"nightly": scheduleOnly, "reusable": callOnly, "manual": dispatchOnly,
	}}

	// A person cannot start a schedule-only workflow, and the error says what
	// it DOES accept rather than only what it refused.
	_, err := e.PrepareRun("nightly", workflow.TriggerDispatch, nil)
	if err == nil {
		t.Fatal("a schedule-only workflow was started by hand")
	}
	if !strings.Contains(err.Error(), "schedule") {
		t.Fatalf("the error should name the declared trigger: %v", err)
	}
	// ...but the clock can.
	if _, err := e.PrepareRun("nightly", workflow.TriggerSchedule, nil); err != nil {
		t.Fatalf("the schedule could not start its own workflow: %v", err)
	}

	// A reusable workflow is invoked, never dispatched.
	if _, err := e.PrepareRun("reusable", workflow.TriggerDispatch, nil); err == nil {
		t.Fatal("a workflow_call-only workflow was started by hand")
	}
	if _, err := e.PrepareRun("reusable", workflow.TriggerWorkflowCall, nil); err != nil {
		t.Fatalf("workflow_call was refused: %v", err)
	}

	// And an inbound POST cannot start something that never invited one.
	if _, err := e.PrepareRun("manual", workflow.TriggerRepositoryDispatch, nil); err == nil {
		t.Fatal("repository_dispatch started a dispatch-only workflow")
	}

	if _, err := e.PrepareRun("missing", workflow.TriggerDispatch, nil); err == nil {
		t.Fatal("an unloaded workflow was started")
	}
}

// A template is a starting point to copy, so it is refused at the same gate
// rather than running and producing a confusing result.
func TestATemplateCannotBeRun(t *testing.T) {
	e := &Engine{defs: map[string]*workflow.Definition{
		"tmpl": {Name: "tmpl", Template: workflow.TemplateInfo{Is: true}, On: workflow.Triggers{Dispatch: true}},
	}}
	_, err := e.PrepareRun("tmpl", workflow.TriggerDispatch, nil)
	if err == nil || !strings.Contains(err.Error(), "copy") {
		t.Fatalf("err = %v, want a refusal that says what to do instead", err)
	}
}

// The scheduler must fire the minute a cron matches, and a workflow whose cron
// does not match must not run. Exercised through the same matching the ticker
// uses, so a passing test means the ticker is right.
func TestSchedulesFireOnTheMatchingMinuteOnly(t *testing.T) {
	c, err := workflow.ParseCron("0 2 * * 1")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-21 is a Monday.
	monday2am := time.Date(2026, 9, 21, 2, 0, 0, 0, time.UTC)
	if !c.Matches(monday2am) {
		t.Fatal("02:00 Monday did not match \"0 2 * * 1\"")
	}
	for _, miss := range []time.Time{
		monday2am.Add(time.Minute),  // 02:01
		monday2am.Add(-time.Minute), // 01:59
		monday2am.AddDate(0, 0, 1),  // Tuesday 02:00
	} {
		if c.Matches(miss) {
			t.Errorf("%s should not have matched", miss)
		}
	}
}
