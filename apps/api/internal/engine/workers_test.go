package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A label this process serves runs here; anything else is placed. That
// decision is the whole of where a step runs, so it is worth stating.
func TestServesLocally(t *testing.T) {
	e := &Engine{}
	e.cfg.RunnerLabels = []string{"local", "self-hosted"}
	for _, c := range []struct {
		label string
		here  bool
	}{
		{"", true},            // no runs-on at all: here, as it always was
		{"local", true},       //
		{"Self-Hosted", true}, // a label is not case sensitive; a typo in caps is not a placement
		{"windows", false},    // somebody else's machine
	} {
		if got := e.servesLocally(c.label); got != c.here {
			t.Errorf("servesLocally(%q) = %v, want %v", c.label, got, c.here)
		}
	}
}

// The point of the whole mechanism: a step with a `runs-on` nobody here serves
// is executed on another machine, and what comes back is INDISTINGUISHABLE
// from a local `run:` step — same fields, so a gate reading `ok` cannot tell.
func TestAStepRunsOnTheMachineThatHoldsItsLabel(t *testing.T) {
	def := &workflow.Definition{Name: "placed", Steps: []workflow.Step{{
		ID: "build", Run: "msbuild /p:Configuration=Release", RunsOn: "windows",
	}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")

	// A machine joins with that label, exactly as `wfx-runner join` does.
	tok, err := h.eng.RegistrationToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined, err := h.eng.Join(context.Background(), JoinRequest{
		Token: tok, Name: "buildbox-" + time.Now().Format("150405.000"),
		Labels: []string{"windows"}, OS: "windows", Arch: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.store.DeleteWorker(context.Background(), joined.Worker.ID) })

	// ...and behaves like one: claim, do the work, report.
	done := make(chan *JobPayload, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		w, err := h.eng.AuthWorker(ctx, joined.Token)
		if err != nil {
			return
		}
		job, err := h.eng.Claim(ctx, w, 20*time.Second)
		if err != nil || job == nil {
			return
		}
		var p JobPayload
		_ = json.Unmarshal(job.Payload, &p)
		res, _ := json.Marshal(JobResult{OK: true, ExitCode: 0, Stdout: "Build succeeded."})
		_ = h.store.FinishJob(ctx, job.ID, w.ID, res)
		done <- &p
	}()

	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}

	var payload *JobPayload
	select {
	case payload = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never saw the job")
	}
	// The worker is sent a rendered command and nothing else it did not need.
	if payload.Command != "msbuild /p:Configuration=Release" {
		t.Errorf("command sent = %q", payload.Command)
	}

	steps, err := h.store.ListSteps(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	for _, s := range steps {
		if s.StepID == "build" {
			if s.Status != "done" {
				t.Fatalf("step = %s (%s)", s.Status, s.Error)
			}
			_ = json.Unmarshal(s.Output, &out)
		}
	}
	if out["ok"] != true || out["stdout"] != "Build succeeded." {
		t.Fatalf("the remote result did not come back as a local one would: %v", out)
	}
	if out["runsOn"] != "windows" {
		t.Errorf("the run does not record where it ran: %v", out["runsOn"])
	}
}

// A `runs-on` nobody serves must FAIL, not hang forever — a typo in a label is
// the common case and it should look like a mistake, not a broken platform.
func TestAnUnservedLabelFailsRatherThanHangs(t *testing.T) {
	def := &workflow.Definition{Name: "unserved", Steps: []workflow.Step{{
		ID: "build", Run: "echo hi", RunsOn: "nobody-holds-this",
	}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")

	// The real ceiling is half an hour — long enough for a laptop to be opened.
	// A test does not need to prove the duration, only that the wait ENDS.
	defer func(d time.Duration) { workerWait = d }(workerWait)
	workerWait = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	step := &def.Steps[0]
	_, err := h.eng.runRemote(ctx, mustRun(t, h), step, "echo hi", step.RunsOn, workflow.TemplateData{})
	if err == nil {
		t.Fatal("waiting for a label nobody serves succeeded")
	}
}

// mustRun creates a bare run row for a test that drives one step directly.
func mustRun(t *testing.T, h *harness) uuid.UUID {
	t.Helper()
	r, err := h.store.CreateRun(context.Background(), "local", h.currentWorkflow(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}
