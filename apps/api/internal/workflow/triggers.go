package workflow

import (
	"fmt"
	"strings"
)

// `on:` says WHAT MAY START a workflow, and it is deliberately the smallest
// useful subset of GitHub Actions' own block:
//
//	on:
//	  workflow_dispatch:              # a person, the UI, `wfx run`, the API
//	  schedule:
//	    - cron: "0 9 * * 1"           # five fields, UTC
//	  repository_dispatch:
//	    types: [bug_reported]         # another system, over the API
//	  workflow_call:                  # another workflow, via jobs.<id>.uses
//
// These are GitHub's OWN trigger names and shapes, not near-variants. The whole
// point of adopting that file is that somebody who knows it does not have to
// learn a second dialect, and a name we invent is a name they have to look up.
//
// Four triggers because those are the four ways work arrives: a person asks,
// the clock says, another system says, or another workflow calls. That is the
// same set Actions settled on, and it is complete enough that we have not
// needed a fifth.
//
// `push` and `pull_request` are deliberately absent. They are a forge's event
// vocabulary, and modelling one forge's is how this stops being portable — a
// repository_dispatch carries whatever the sender sends, and a forge's own
// Actions workflow can POST to us in three lines.
//
// AUTHENTICATION IS NOT DECLARED HERE. Actions does not put a credential in the
// `on:` block — repository_dispatch is gated by the API token the caller
// presents — and neither do we. An earlier draft of this file invented a
// `secret_env` field for it; that was a name nobody would recognise, solving a
// problem that belongs to the endpoint. Inbound auth is the API's concern.
//
// The default matters more than the syntax: a workflow with no `on:` is
// dispatch-only. Every workflow that existed before this keeps working, and
// nothing starts firing on a timer because a file gained a field.

// Triggers is the parsed `on:` block.
type Triggers struct {
	// Dispatch is set when a person or the API may start this workflow.
	Dispatch bool `yaml:"-" json:"dispatch"`
	// Schedule holds the cron entries; empty means never on a timer.
	Schedule []Schedule `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	// RepositoryDispatch, when non-nil, lets an inbound API call start this
	// workflow — Actions' own name for the hook case.
	RepositoryDispatch *RepositoryDispatch `yaml:"repository_dispatch,omitempty" json:"repositoryDispatch,omitempty"`
	// Call is set when another workflow may invoke this one through
	// `jobs.<id>.uses`. Actions spells it `workflow_call`, and it is what makes
	// a workflow reusable rather than merely copyable.
	Call bool `yaml:"-" json:"workflowCall"`
}

// Schedule is one cron entry. Five fields, UTC, because a schedule that means
// something different depending on where the server is deployed is a bug
// waiting for a daylight-saving boundary.
type Schedule struct {
	Cron string `yaml:"cron" json:"cron"`
	// Input is the input the scheduled run starts with. A timer has nobody to
	// fill a form, so anything the workflow requires has to be written here.
	Input map[string]any `yaml:"input,omitempty" json:"input,omitempty"`
}

// RepositoryDispatch is an inbound trigger: another system POSTs and a run
// starts. Actions calls the payload `client_payload`, and so do we.
type RepositoryDispatch struct {
	// Types are the event names this workflow answers, exactly as in Actions:
	// the caller sends an `event_type` and only a workflow listing it runs.
	// Empty means every event type, which is also Actions' behaviour.
	Types []string `yaml:"types,omitempty" json:"types,omitempty"`
}

// Accepts reports whether this workflow answers that event type.
func (r RepositoryDispatch) Accepts(eventType string) bool {
	if len(r.Types) == 0 {
		return true
	}
	for _, t := range r.Types {
		if t == eventType {
			return true
		}
	}
	return false
}

// TriggerKind is how a run was started. It is recorded on the run, so "why did
// this run at 3am" has an answer.
type TriggerKind string

const (
	TriggerDispatch           TriggerKind = "workflow_dispatch"
	TriggerSchedule           TriggerKind = "schedule"
	TriggerRepositoryDispatch TriggerKind = "repository_dispatch"
	TriggerWorkflowCall       TriggerKind = "workflow_call"
)

// Allows reports whether this workflow may be started by that trigger.
//
// It is enforced on run creation, not merely documented. A declared trigger
// that nothing checks is the failure this project keeps meeting: a field that
// expresses intent is not a control (ADR 0004, ADR 0006).
func (t Triggers) Allows(k TriggerKind) bool {
	// An empty block is dispatch-only. The default lives HERE rather than only
	// in validate(), because a Definition built in code — a test, the UI's JSON
	// path — would otherwise be startable by nothing at all, and the failure
	// reads as "it declares `on: `", which explains nothing.
	if t.none() {
		return k == TriggerDispatch
	}
	switch k {
	case TriggerDispatch:
		return t.Dispatch
	case TriggerSchedule:
		return len(t.Schedule) > 0
	case TriggerRepositoryDispatch:
		return t.RepositoryDispatch != nil
	case TriggerWorkflowCall:
		return t.Call
	}
	return false
}

// none reports a completely unset block.
func (t Triggers) none() bool {
	return !t.Dispatch && len(t.Schedule) == 0 && t.RepositoryDispatch == nil && !t.Call
}

// Names lists the triggers this workflow declares, for an error message.
func (t Triggers) Names() []string {
	if t.none() {
		return []string{string(TriggerDispatch)}
	}
	var out []string
	if t.Dispatch {
		out = append(out, string(TriggerDispatch))
	}
	if len(t.Schedule) > 0 {
		out = append(out, string(TriggerSchedule))
	}
	if t.RepositoryDispatch != nil {
		out = append(out, string(TriggerRepositoryDispatch))
	}
	if t.Call {
		out = append(out, string(TriggerWorkflowCall))
	}
	return out
}

// UnmarshalYAML accepts the shapes GitHub Actions accepts, because a person who
// knows that file should not have to learn a second dialect:
//
//	on: workflow_dispatch          # a single name
//	on: [workflow_dispatch, ...]   # a list of names
//	on: {workflow_dispatch: null, schedule: [...]}   # a mapping
//
// A bare name or a list has no room for a cron expression or a secret, so those
// forms only enable dispatch.
func (t *Triggers) UnmarshalYAML(unmarshal func(any) error) error {
	var one string
	if err := unmarshal(&one); err == nil && one != "" {
		return t.enable(one)
	}
	var list []string
	if err := unmarshal(&list); err == nil && len(list) > 0 {
		for _, name := range list {
			if err := t.enable(name); err != nil {
				return err
			}
		}
		return nil
	}

	var m struct {
		WorkflowDispatch   any                 `yaml:"workflow_dispatch"`
		Dispatch           any                 `yaml:"dispatch"`
		Schedule           []Schedule          `yaml:"schedule"`
		RepositoryDispatch *RepositoryDispatch `yaml:"repository_dispatch"`
	}
	if err := unmarshal(&m); err != nil {
		return fmt.Errorf("`on` must be a trigger name, a list of names, or a mapping: %w", err)
	}
	// A mapping KEY being present is the signal, exactly as in Actions, where
	// `workflow_dispatch:` with no value is the normal spelling. yaml gives a
	// nil value either way, so presence is detected by re-reading the raw keys.
	var keys map[string]any
	if err := unmarshal(&keys); err == nil {
		for k := range keys {
			switch k {
			case "workflow_dispatch", "dispatch":
				t.Dispatch = true
			case "schedule", "repository_dispatch":
			case "workflow_call":
				t.Call = true
			// Actions declares a dispatch form under workflow_dispatch.inputs;
			// ours is the richer `input_schema` at the top level, so the key is
			// accepted and ignored rather than rejected — a file copied from a
			// repository should load.
			case "inputs":
			default:
				return fmt.Errorf("unknown trigger %q — supported: workflow_dispatch, schedule, "+
					"repository_dispatch, workflow_call", k)
			}
		}
	}
	t.Schedule = m.Schedule
	t.RepositoryDispatch = m.RepositoryDispatch
	return nil
}

func (t *Triggers) enable(name string) error {
	switch strings.TrimSpace(name) {
	case "workflow_dispatch", "dispatch":
		t.Dispatch = true
		return nil
	case "schedule":
		return fmt.Errorf("`schedule` needs a cron expression, so it cannot be given as a bare name")
	case "repository_dispatch":
		// Actions accepts a bare `repository_dispatch`, meaning every event
		// type, so we do too.
		t.RepositoryDispatch = &RepositoryDispatch{}
		return nil
	case "workflow_call":
		t.Call = true
		return nil
	}
	return fmt.Errorf("unknown trigger %q — supported: workflow_dispatch, schedule, "+
		"repository_dispatch, workflow_call", name)
}

// validateTriggers is called at load time. It checks the shape only — whether
// the secret variable is actually SET is a property of the machine, reported by
// `wfx doctor`, because a workflow is authored on one machine and run on
// another.
func validateTriggers(name string, t *Triggers) error {
	for i, s := range t.Schedule {
		if strings.TrimSpace(s.Cron) == "" {
			return fmt.Errorf("%s: schedule[%d] has no cron expression", name, i)
		}
		if err := ValidateCron(s.Cron); err != nil {
			return fmt.Errorf("%s: schedule[%d]: %w", name, i, err)
		}
	}
	return nil
}
