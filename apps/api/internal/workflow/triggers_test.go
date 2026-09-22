package workflow

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every shape GitHub Actions accepts for `on:` must load here, because the
// whole point of adopting that file is that a person who knows it does not have
// to learn a second dialect.
func TestOnAcceptsTheActionsShapes(t *testing.T) {
	cases := map[string]func(*testing.T, Triggers){
		// a bare name
		"on: workflow_dispatch": func(t *testing.T, tr Triggers) {
			if !tr.Allows(TriggerDispatch) || tr.Allows(TriggerSchedule) {
				t.Errorf("got %v", tr.Names())
			}
		},
		// a list of names
		"on: [workflow_dispatch, repository_dispatch]": func(t *testing.T, tr Triggers) {
			if !tr.Allows(TriggerDispatch) || !tr.Allows(TriggerRepositoryDispatch) {
				t.Errorf("got %v", tr.Names())
			}
		},
		// a mapping with an empty value, which is Actions' normal spelling
		"on:\n  workflow_dispatch:\n  schedule:\n    - cron: \"0 9 * * 1\"": func(t *testing.T, tr Triggers) {
			if !tr.Allows(TriggerDispatch) || !tr.Allows(TriggerSchedule) {
				t.Errorf("got %v", tr.Names())
			}
			if len(tr.Schedule) != 1 || tr.Schedule[0].Cron != "0 9 * * 1" {
				t.Errorf("schedule = %+v", tr.Schedule)
			}
		},
		// repository_dispatch with types, and workflow_call
		"on:\n  repository_dispatch:\n    types: [bug_reported]\n  workflow_call:": func(t *testing.T, tr Triggers) {
			if !tr.Allows(TriggerRepositoryDispatch) || !tr.Allows(TriggerWorkflowCall) {
				t.Errorf("got %v", tr.Names())
			}
			if !tr.RepositoryDispatch.Accepts("bug_reported") || tr.RepositoryDispatch.Accepts("other") {
				t.Error("types not honoured")
			}
			// A person cannot start it: dispatch was never declared.
			if tr.Allows(TriggerDispatch) {
				t.Error("dispatch was not declared and must not be allowed")
			}
		},
		// Actions declares dispatch inputs under the trigger; ours live in
		// input_schema, so the key must be tolerated, not rejected.
		"on:\n  workflow_dispatch:\n    inputs:\n      why:\n        required: true": func(t *testing.T, tr Triggers) {
			if !tr.Allows(TriggerDispatch) {
				t.Errorf("got %v", tr.Names())
			}
		},
	}
	for src, check := range cases {
		t.Run(src, func(t *testing.T) {
			var d struct {
				On Triggers `yaml:"on"`
			}
			if err := yaml.Unmarshal([]byte(src), &d); err != nil {
				t.Fatalf("%s: %v", src, err)
			}
			check(t, d.On)
		})
	}
}

// A name Actions does not have must be refused, not quietly ignored — a typo
// that silently disables every trigger is the worst outcome.
func TestOnRefusesInventedTriggers(t *testing.T) {
	for _, src := range []string{
		"on: webhook",
		"on: push",
		"on:\n  webhook:\n    secret_env: X",
		"on: [workflow_dispatch, cron]",
	} {
		var d struct {
			On Triggers `yaml:"on"`
		}
		err := yaml.Unmarshal([]byte(src), &d)
		if err == nil {
			t.Errorf("%q was accepted; it declares %v", src, d.On.Names())
		}
	}
}

// An absent `on:` is dispatch-only. Two reasons this is asserted: a workflow
// written before `on:` existed must keep working, and nothing may start firing
// on a timer because a file gained a field.
func TestNoOnBlockMeansDispatchOnly(t *testing.T) {
	var tr Triggers
	if !tr.Allows(TriggerDispatch) {
		t.Error("an empty block must allow dispatch")
	}
	for _, k := range []TriggerKind{TriggerSchedule, TriggerRepositoryDispatch, TriggerWorkflowCall} {
		if tr.Allows(k) {
			t.Errorf("an empty block must not allow %s", k)
		}
	}
	if got := tr.Names(); len(got) != 1 || got[0] != string(TriggerDispatch) {
		t.Errorf("Names() = %v", got)
	}
}

// A cron expression is checked at LOAD time: a schedule that silently never
// fires is indistinguishable from a working one until somebody notices the work
// is not happening.
func TestABadCronIsRefusedAtLoad(t *testing.T) {
	tr := &Triggers{Schedule: []Schedule{{Cron: "0 9 * * MON"}}}
	err := validateTriggers("wf", tr)
	if err == nil {
		t.Fatal("a named weekday was accepted")
	}
	if !strings.Contains(err.Error(), "schedule[0]") {
		t.Fatalf("the error should say which entry: %v", err)
	}
	if err := validateTriggers("wf", &Triggers{Schedule: []Schedule{{Cron: ""}}}); err == nil {
		t.Fatal("an empty cron was accepted")
	}
}
