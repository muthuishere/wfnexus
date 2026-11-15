package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Each test here drives ONE scrub layer directly, around the others, so that
// removing that layer alone fails it.

// r4Run is a run row with one step row and r4Secret registered, in a harness
// whose own workflow is never started.
func r4Run(t *testing.T) (*harness, uuid.UUID) {
	t.Helper()
	h := newHarness(t, &workflow.Definition{Name: "r4-" + uuid.NewString()[:8],
		Steps: []workflow.Step{{ID: "s", Run: "true"}}}, newFakeLLM(t), "")
	ctx := context.Background()
	run, err := h.store.CreateRun(ctx, "local", h.currentWorkflow(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.EnsureStep(ctx, run.ID, "s", 0); err != nil {
		t.Fatal(err)
	}
	h.eng.redactions.add(run.ID, map[string]string{"R4_TOKEN": r4Secret}, nil)
	return h, run.ID
}

func r4Clean(t *testing.T, where, got string) {
	t.Helper()
	if run := anyRun(got, r4Secret, minCutPrefix); run != "" {
		t.Fatalf("%s: %q survived: %s", where, run, got)
	}
}

// setStep: the output and the pending question are scrubbed before the step
// row is written.
func TestSetStepScrubsOutputAndPending(t *testing.T) {
	h, id := r4Run(t)
	h.eng.setStep(context.Background(), id, "s", model.StepPatch{
		Output:  mustJSON(map[string]any{"found": "key " + r4Secret}),
		Pending: mustJSON(tn.Request{ID: "q", Kind: "input", Prompt: "is " + r4Secret + " right?"}),
	})
	st := h.steps(id)["s"]
	r4Clean(t, "output", string(st.Output))
	r4Clean(t, "pending", string(st.Pending))
	if !strings.Contains(string(st.Output), "[redacted:R4_TOKEN]") || !strings.Contains(string(st.Pending), "[redacted:R4_TOKEN]") {
		t.Fatalf("output %s, pending %s", st.Output, st.Pending)
	}
}

// setRun: the run's error is scrubbed; a run that ends starts the grace
// period, after which its secrets leave memory; a parked run keeps them.
func TestSetRunScrubsItsErrorAndEndsTheRunsSecrets(t *testing.T) {
	h, id := r4Run(t)
	clk := &fakeClock{t: time.Now()}
	h.eng.redactions.now = clk.now
	ctx := context.Background()
	parked, _ := h.store.CreateRun(ctx, "local", h.currentWorkflow(), json.RawMessage(`{}`))
	h.eng.redactions.add(parked.ID, map[string]string{"R4_TOKEN": r4Secret}, nil)
	h.eng.setRun(ctx, parked.ID, "needs_input", "s", "")

	h.eng.setRun(ctx, id, "failed", "s", "boom "+r4Secret)
	run, _ := h.store.GetRun(ctx, id)
	r4Clean(t, "run error", run.Error)
	if len(h.eng.redactions.get(id)) == 0 {
		t.Fatal("dropped at once: a late write would not be scrubbed")
	}
	clk.add(endedSecretsGrace + time.Second)
	h.eng.redactions.sweep()
	if len(h.eng.redactions.get(id)) != 0 {
		t.Fatal("an ended run's secrets are still held after the grace period")
	}
	if len(h.eng.redactions.get(parked.ID)) == 0 {
		t.Fatal("a parked run's secrets were dropped")
	}

	// A run that ended and then runs again (a retry) is not dropped.
	again, _ := h.store.CreateRun(ctx, "local", h.currentWorkflow(), json.RawMessage(`{}`))
	h.eng.redactions.add(again.ID, map[string]string{"R4_TOKEN": r4Secret}, nil)
	h.eng.setRun(ctx, again.ID, "failed", "s", "")
	h.eng.setRun(ctx, again.ID, "running", "s", "")
	clk.add(endedSecretsGrace + time.Second)
	h.eng.redactions.sweep()
	if len(h.eng.redactions.get(again.ID)) == 0 {
		t.Fatal("a run running again was dropped")
	}
}

// Values registered again for a worker's late report on a run that already
// ended get the grace period too: they do not stay in memory for good.
func TestValuesRegisteredForAnEndedRunAreDropped(t *testing.T) {
	h, id := r4Run(t)
	clk := &fakeClock{t: time.Now()}
	h.eng.redactions.now = clk.now
	ctx := context.Background()
	h.eng.setRun(ctx, id, "done", "s", "")
	clk.add(endedSecretsGrace + time.Second)
	h.eng.redactions.sweep()
	payload, _ := json.Marshal(JobPayload{Env: map[string]string{"R4_TOKEN": r4Secret}})
	h.eng.IngestWorkerEvents(ctx, id, payload, []WorkerEvent{{StepID: "s", Payload: map[string]any{"text": "late " + r4Secret}}})
	if r4Clean(t, "late event", eventTexts(t, h.store, id)); len(h.eng.redactions.get(id)) == 0 {
		t.Fatal("not registered again for the late report")
	}
	clk.add(endedSecretsGrace + time.Second)
	h.eng.redactions.sweep()
	if len(h.eng.redactions.get(id)) != 0 {
		t.Fatal("values registered for an ended run stay in memory")
	}
}

// notifyPause: the prompt and the request's link are scrubbed before they
// are sent.
func TestNotifyPauseScrubsWhatItSends(t *testing.T) {
	h, id := r4Run(t)
	var mu sync.Mutex
	var got []string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p notify.Pause
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		got = append(got, p.Kind+" | "+p.Prompt+" | "+p.URL)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer sink.Close()
	h.eng.catalog.Notifiers.Add(catalog.Notifier{Name: "r4sink", Kind: "webhook", URL: sink.URL}, "test")
	h.eng.notifyPause(context.Background(), id, "s", "input "+r4Secret, "is "+r4Secret+" right?",
		&tn.Request{ID: "q", Kind: "input", URL: "https://example.test/answer?t=" + r4Secret})
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("sent %d", len(got))
	}
	r4Clean(t, "pause", got[0])
}

// runOneStep: an agent's output is scrubbed before the steps after it read
// it (not only where it is stored).
func TestAnAgentsOutputIsScrubbedBeforeTheNextStepReadsIt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "seen")
	def := &workflow.Definition{Name: "r4-next-" + uuid.NewString()[:8], Steps: []workflow.Step{
		{ID: "a", Prompt: "find it", Env: map[string]string{"R4_TOKEN": r4Secret},
			OutputSchema: objSchema([]any{"finding"}, map[string]any{"finding": strProp()})},
		{ID: "b", Run: `printf '%s' '{{ .Steps.a.finding }}' > ` + file},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t, submit(map[string]any{"finding": "saw " + r4Secret}), finish()), "")
	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("run %s: %s", run.Status, run.Error)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if r4Clean(t, "next step", string(b)); !strings.Contains(string(b), "[redacted:R4_TOKEN]") {
		t.Fatalf("next step read %q", b)
	}
}

// saveRemoteArtifacts: a worker's final text and diff are scrubbed before they
// are stored, whatever reached it.
func TestRemoteArtifactsAreScrubbed(t *testing.T) {
	h, id := r4Run(t)
	blob := &memBlob{}
	h.eng.blob = blob
	h.eng.saveRemoteArtifacts(context.Background(), id, "s", &AgentOutcome{
		RawText: "final " + r4Secret, Diff: []byte("+token = \"" + r4Secret + "\"\n"),
	})
	if len(blob.got) != 2 {
		t.Fatalf("artifacts: %d", len(blob.got))
	}
	for _, b := range blob.got {
		r4Clean(t, "artifact", b)
	}
}

// settleOutcome: the error is scrubbed BEFORE the outcome line is cut from it,
// and the derived line is scrubbed again, which also covers step rows that
// were written without setStep.
func TestSettleOutcomeScrubsTheErrorAndTheLine(t *testing.T) {
	ctx := context.Background()
	// The error straddles the outcome line's cut: scrubbing after the cut
	// alone keeps the first characters.
	for _, off := range []int{2, 5} {
		h, id := r4Run(t)
		msg := strings.Repeat("e", maxOutcomeLen-len("failed: ")-off) + r4Secret + " tail"
		h.eng.settleOutcome(ctx, id, "failed", msg)
		run, _ := h.store.GetRun(ctx, id)
		if k := pieceBefore(run.Outcome, "…", r4Secret); k > 0 {
			t.Fatalf("off=%d: %d characters kept: %q", off, k, run.Outcome)
		}
	}
	// A step row stored raw (not through setStep): the line is scrubbed.
	h, id := r4Run(t)
	if err := h.store.PatchStep(ctx, id, "s", store.StepPatch{Status: str("done"),
		Output: mustJSON(map[string]any{"outcome": "ok: deployed with " + r4Secret})}); err != nil {
		t.Fatal(err)
	}
	h.eng.settleOutcome(ctx, id, "done", "")
	run, _ := h.store.GetRun(ctx, id)
	if r4Clean(t, "outcome", run.Outcome); !strings.Contains(run.Outcome, "[redacted:R4_TOKEN]") {
		t.Fatalf("outcome %q", run.Outcome)
	}
}

// failStep: the run's secrets are removed before the identifier patterns, so
// a pattern cannot take part of a secret and leave the rest.
func TestFailStepScrubsSecretsBeforeIdentifierPatterns(t *testing.T) {
	const shaped = "zz9Kq7Wv_user_id=Wx3Lm9Pt"
	h, id := r4Run(t)
	h.eng.redactions.add(id, map[string]string{"SHAPED_TOKEN": shaped}, nil)
	h.eng.failStep(context.Background(), id, "s", errors.New("bad "+shaped))
	run, _ := h.store.GetRun(context.Background(), id)
	for what, got := range map[string]string{"step error": h.steps(id)["s"].Error, "run error": run.Error} {
		if strings.Contains(got, "zz9Kq7") {
			t.Fatalf("%s keeps part of the secret: %q", what, got)
		}
	}
}

// persistStepState: a `state:` value rendered from the run's data is
// scrubbed before it is kept beyond the run.
func TestAStateBlockValueIsScrubbed(t *testing.T) {
	h, id := r4Run(t)
	def := &workflow.Definition{Name: "r4-state-" + uuid.NewString()[:8]}
	step := &workflow.Step{ID: "s", State: map[string]map[string]string{
		model.StateScopeWorkflow: {"seen": "{{ .Input.tok }}"},
	}}
	ctx := context.Background()
	t.Cleanup(func() { _ = h.eng.DeleteState(ctx, model.StateScopeWorkflow, def.Name, "seen") })
	h.eng.persistStepState(ctx, def, step, workflow.TemplateData{RunID: id.String(),
		Input: map[string]any{"tok": "x " + r4Secret}}, map[string]any{})
	v, ok, err := h.eng.GetState(ctx, model.StateScopeWorkflow, def.Name, "seen")
	if err != nil || !ok {
		t.Fatalf("state: %v %v", ok, err)
	}
	if r4Clean(t, "state", v); !strings.Contains(v, "[redacted:R4_TOKEN]") {
		t.Fatalf("state %q", v)
	}
}

// runSend: the text and a file attachment are scrubbed before they leave.
func TestASendStepScrubsItsTextAndAttachment(t *testing.T) {
	h, id := r4Run(t)
	dir := fakeChannel(t, h.eng, "r4send", false)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "report.txt"), []byte("token: "+r4Secret+"\n"), 0o600)
	step := &workflow.Step{ID: "s", Send: &workflow.SendStep{Channel: "r4send", Text: "found {{ .Input.tok }}", Attachment: "report.txt"}}
	if _, err := h.eng.runSend(context.Background(), id, step, workflow.TemplateData{
		RunID: id.String(), WorkDir: ws, Input: map[string]any{"tok": r4Secret}}); err != nil {
		t.Fatal(err)
	}
	sent := sentEnvelopes(t, dir)
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
	r4Clean(t, "text", sent[0].Text)
	att, err := os.ReadFile(filepath.Join(dir, "attached.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if r4Clean(t, "attachment", string(att)); !strings.Contains(string(att), "[redacted:R4_TOKEN]") {
		t.Fatalf("attachment %q", att)
	}
}

// runRemote: a worker's report that reached the job row unscrubbed (written
// straight to the store, around finishJob) is still scrubbed whole before
// the step's output is cut.
func TestARemoteStepsOutputIsScrubbedBeforeItIsCut(t *testing.T) {
	for _, off := range []int{2, 3} {
		label := "r4-" + uuid.NewString()[:8]
		def := &workflow.Definition{Name: "r4-remote-" + uuid.NewString()[:8], Steps: []workflow.Step{{
			ID: "s", Run: "build", RunsOn: label, Env: map[string]string{"R4_TOKEN": r4Secret},
		}}}
		normalizeForTest(def)
		h := newHarness(t, def, newFakeLLM(t), "")
		ctx := context.Background()
		tok, err := h.eng.RegistrationToken(ctx)
		if err != nil {
			t.Fatal(err)
		}
		joined, err := h.eng.Join(ctx, JoinRequest{Token: tok, Name: "r4box-" + uuid.NewString()[:8], Labels: []string{label}, OS: "linux", Arch: "amd64"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.store.DeleteWorker(context.Background(), joined.Worker.ID) })
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
			out := strings.Repeat(".", maxEventOutput-off) + r4Secret + " tail"
			res, _ := json.Marshal(JobResult{OK: true, Stdout: out, Stderr: out})
			_ = h.store.FinishJob(ctx, job.ID, w.ID, res)
		}()
		run := h.run(nil)
		if run.Status != "done" {
			t.Fatalf("run %s: %s", run.Status, run.Error)
		}
		o := output(t, h.steps(run.ID)["s"])
		for _, k := range []string{"stdout", "stderr"} {
			s := fmt.Sprint(o[k])
			if n := pieceBefore(s, "\n… (", r4Secret); n > 0 {
				t.Fatalf("off=%d %s keeps %d characters: %q", off, k, n, s[len(s)-60:])
			}
		}
		res := eventTexts(t, h.store, run.ID)
		if n := pieceBefore(res, `\n… (`, r4Secret); n > 0 { // as JSON spells it
			t.Fatalf("off=%d an event keeps %d characters", off, n)
		}
	}
}
