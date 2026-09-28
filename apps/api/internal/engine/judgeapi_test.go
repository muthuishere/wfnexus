package engine

import (
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/judge"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A local step is told how to reach the server that started it, because the
// server's own WFX_* configuration is scrubbed from what it inherits — a `wfx
// judge` inside a step otherwise found no registry (2026-09-28 session).
func TestALocalStepIsGivenTheWayBackToTheServer(t *testing.T) {
	runID := uuid.New()
	e := &Engine{cfg: config.Config{Addr: ":8090"}, attached: map[uuid.UUID]mountState{runID: {}}}
	got, err := e.stepEnv(t.Context(), runID, &workflow.Step{ID: "s"}, "/ws")
	if err != nil {
		t.Fatal(err)
	}
	if got["WFX_API"] != "http://127.0.0.1:8090" {
		t.Fatalf("WFX_API = %q, want the server's loopback address", got["WFX_API"])
	}
	// A workflow that sets WFX_API on purpose still wins.
	got, _ = e.stepEnv(t.Context(), runID, &workflow.Step{ID: "s", Env: map[string]string{"WFX_API": "http://other:1"}}, "/ws")
	if got["WFX_API"] != "http://other:1" {
		t.Fatalf("the file did not win: %q", got["WFX_API"])
	}
	// A packed step gets the worker's URL from the worker, not this one.
	got, _ = e.stepEnv(t.Context(), runID, &workflow.Step{ID: "s"}, "")
	if _, ok := got["WFX_API"]; ok {
		t.Fatal("a step packed for a worker must not carry this machine's loopback URL")
	}
}

func TestSelfURL(t *testing.T) {
	for addr, want := range map[string]string{
		":8090":          "http://127.0.0.1:8090",
		"0.0.0.0:8090":   "http://127.0.0.1:8090",
		"127.0.0.1:9000": "http://127.0.0.1:9000",
		"[::]:8090":      "http://127.0.0.1:8090",
		"":               "",
		"garbage":        "",
	} {
		if got := selfURL(addr); got != want {
			t.Errorf("selfURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

// Judge answers on the server's classifier and returns the same banded shape
// `wfx judge` prints.
func TestJudgeAnswersOnTheServersClassifier(t *testing.T) {
	step := &workflow.Step{ID: "s", Decide: &workflow.Decide{
		State: "func actorOf has no callers",
		Questions: map[string]workflow.Question{
			"dead": {Type: "noul", Instructions: "Is this unreachable?"},
		},
	}}
	e := &Engine{}
	e.UseClassifier(recordDecision(t, step, 0, 0.9))
	res, _, err := e.Judge(t.Context(), "", step.Decide.Questions,
		[]JudgeItem{{ID: "a", State: step.Decide.State}}, judge.DefaultBands, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Error != "" || res[0].Answers["dead"].Band != "yes" {
		t.Fatalf("got %+v", res)
	}
}
