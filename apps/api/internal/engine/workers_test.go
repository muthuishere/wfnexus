package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"

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
	label := "windows-" + uuid.NewString()[:8]
	def := &workflow.Definition{Name: "placed", Steps: []workflow.Step{{
		ID: "build", Run: "msbuild /p:Configuration=Release", RunsOn: label,
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
		Labels: []string{label}, OS: "windows", Arch: "amd64",
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
	if out["runsOn"] != label {
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

// The whole point of option A: a `prompt:` step placed on a worker runs THERE,
// as the same agent, with its skills carried to that machine — and what comes
// back is a schema-validated output the run records like any other.
func TestAnAgentStepRunsOnTheWorkerWithItsSkills(t *testing.T) {
	// A skill that exists only on the platform. The worker has no skills
	// directory, so if the step can load it, it travelled.
	skillRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skillRoot, "counting"), 0o755); err != nil {
		t.Fatal(err)
	}
	const skillBody = "---\nname: counting\ndescription: \"How this shop counts things.\"\n---\n# Counting\n\nAlways count in dozens.\n"
	if err := os.WriteFile(filepath.Join(skillRoot, "counting", "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// A label unique to this run. The store is shared with anything else
	// pointed at it — another test, a real worker somebody left polling — and a
	// stranger claiming the job is a confusing way to fail.
	label := "buildbox-" + uuid.NewString()[:8]
	def := &workflow.Definition{Name: "placed-agent", Steps: []workflow.Step{{
		ID: "think", Prompt: "count the things", RunsOn: label,
		Skills: []string{"counting"}, MaxTurns: 4,
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)

	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, skillRoot)

	tok, err := h.eng.RegistrationToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined, err := h.eng.Join(context.Background(), JoinRequest{
		Token: tok, Name: "agentbox-" + time.Now().Format("150405.000"),
		Labels: []string{label}, OS: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.store.DeleteWorker(context.Background(), joined.Worker.ID) })

	// A worker with NO skills registry of its own, running the same engine code
	// the platform runs.
	gotSkill := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		w, err := h.eng.AuthWorker(ctx, joined.Token)
		if err != nil {
			return
		}
		j, err := h.eng.Claim(ctx, w, 20*time.Second)
		if err != nil || j == nil {
			return
		}
		var p JobPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil || !p.IsAgent() {
			gotSkill <- "the job did not arrive as an agent job"
			return
		}
		var carried string
		for _, f := range p.Agent.Skills {
			if strings.Contains(string(f.Body), "Always count in dozens") {
				carried = f.Path
			}
		}
		gotSkill <- carried

		eng := NewWorkerEngine(func(string, any) {})
		out := eng.RunAgentJob(ctx, p.Agent, t.TempDir())
		raw, _ := json.Marshal(JobResult{OK: out.Error == "", Agent: &out})
		_ = h.store.FinishJob(ctx, j.ID, w.ID, raw)
	}()

	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	select {
	case p := <-gotSkill:
		if p != "counting/SKILL.md" {
			t.Fatalf("the skill did not travel with the step: %q", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the worker never reported what it was sent")
	}

	steps, err := h.store.ListSteps(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.StepID != "think" {
			continue
		}
		if s.Status != "done" {
			t.Fatalf("step = %s (%s)", s.Status, s.Error)
		}
		var out map[string]any
		_ = json.Unmarshal(s.Output, &out)
		if out["ok"] != true {
			t.Fatalf("the remote agent's validated output did not come back: %v", out)
		}
	}
}

// A step that reaches back into the platform — the workflow catalogue, the
// validator, the dry run — cannot be placed, and says so at pack time rather
// than as a missing tool twenty turns into somebody else's machine.
func TestAPlatformToolStepCannotBePlaced(t *testing.T) {
	def := &workflow.Definition{Name: "authoring-placed", Steps: []workflow.Step{{
		ID: "write", Prompt: "author it", RunsOn: "buildbox",
		Tools:        []string{skills.ToolCatalog},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")

	_, err := h.eng.packAgentJob(mustRun(t, h), def, &def.Steps[0], "author it", "")
	if err == nil {
		t.Fatal("a step using a platform tool was packed for another machine")
	}
	if !strings.Contains(err.Error(), skills.ToolCatalog) {
		t.Fatalf("the refusal does not name the tool: %v", err)
	}
}

// Re-running the join command on a machine that already joined must UPDATE it.
// Without this every reinstall and every service restart that re-joins leaves a
// ghost, and the Workers page fills with offline machines that do not exist —
// which is what three rows for one laptop looked like in practice.
func TestRejoiningUpdatesTheMachineRatherThanAddingAnother(t *testing.T) {
	h := newHarness(t, &workflow.Definition{Name: "rejoin", Steps: []workflow.Step{{
		ID: "noop", Run: "true",
	}}}, newFakeLLM(t), "")
	ctx := context.Background()
	tok, err := h.eng.RegistrationToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := "rejoiner-" + uuid.NewString()[:8]

	first, err := h.eng.Join(ctx, JoinRequest{Token: tok, Name: name, Labels: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.store.DeleteWorker(ctx, first.Worker.ID) })

	second, err := h.eng.Join(ctx, JoinRequest{Token: tok, Name: name, Labels: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Worker.ID != first.Worker.ID {
		t.Fatalf("re-joining made a second machine: %s then %s", first.Worker.ID, second.Worker.ID)
	}

	var seen int
	for _, w := range mustWorkers(t, h) {
		if w.Name == name {
			seen++
			if len(w.Labels) != 2 {
				t.Errorf("the new labels did not replace the old: %v", w.Labels)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("%d rows named %q; want 1", seen, name)
	}

	// The old token must stop working: whoever just ran the join command holds
	// the new one, and two processes claiming to be one machine would race for
	// its jobs.
	if _, err := h.eng.AuthWorker(ctx, first.Token); err == nil {
		t.Error("the superseded token still authenticates")
	}
	if _, err := h.eng.AuthWorker(ctx, second.Token); err != nil {
		t.Errorf("the new token does not authenticate: %v", err)
	}
}

func mustWorkers(t *testing.T, h *harness) []*store.Worker {
	t.Helper()
	ws, err := h.store.ListWorkers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ws
}
