package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// These run on Postgres (the harness's pgtest database).

// valueForms are the spellings a delivered value must be found in nowhere.
func valueForms(v string) map[string]string {
	return map[string]string{
		"raw": v, "base64": base64.StdEncoding.EncodeToString([]byte(v)),
		"hex": hex.EncodeToString([]byte(v)), "HEX": strings.ToUpper(hex.EncodeToString([]byte(v))),
	}
}

// pgDump is every row of every table of this database, as JSON text.
func pgDump(t *testing.T, st *store.Store) string {
	t.Helper()
	db := st.DB()
	rows, err := db.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var b strings.Builder
	for _, tb := range tables {
		r, err := db.Query(`SELECT row_to_json(x)::text FROM "` + tb + `" x`)
		if err != nil {
			t.Fatalf("%s: %v", tb, err)
		}
		for r.Next() {
			var s string
			_ = r.Scan(&s)
			b.WriteString(tb + ": " + s + "\n")
		}
		r.Close()
	}
	return b.String()
}

// noForm fails when any form of any value is in any of the places.
func noForm(t *testing.T, when string, places map[string]string, values ...string) {
	t.Helper()
	for _, v := range values {
		for form, s := range valueForms(v) {
			for where, text := range places {
				if strings.Contains(text, s) {
					t.Errorf("%s: the %s form of a delivered value is in %s", when, form, where)
				}
			}
		}
	}
}

// queuedJob waits for the run's job to be queued and returns its id.
func queuedJob(t *testing.T, st *store.Store, runID uuid.UUID) uuid.UUID {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var id string
		err := st.DB().QueryRow(`SELECT id::text FROM worker_jobs WHERE run_id = $1 AND status = 'queued'`, runID).Scan(&id)
		if err == nil {
			return uuid.MustParse(id)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no job was queued")
	return uuid.Nil
}

func joinWorker(t *testing.T, h *harness, label string) *model.Worker {
	t.Helper()
	ctx := context.Background()
	tok, err := h.eng.RegistrationToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := h.eng.Join(ctx, JoinRequest{Token: tok, Name: "deliver-" + uuid.NewString()[:8], Labels: []string{label}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.store.DeleteWorker(context.Background(), joined.Worker.ID) })
	w, err := h.eng.AuthWorker(ctx, joined.Token)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// A value rotated in the env store between enqueue and claim: the claim
// delivers the NEW value, and that value — in every form, in a worker's
// report, its events, its error and the server log — is stored nowhere. Nor
// is the old one.
func TestARotatedValueIsDeliveredAndScrubbedEverywhere(t *testing.T) {
	label := "deliver-" + uuid.NewString()[:8]
	def := &workflow.Definition{Name: "deliver-rotate", Steps: []workflow.Step{{ID: "use", Run: "echo hi", RunsOn: label}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	ctx := context.Background()
	key := "DELIVER_ROTATE_" + strings.ToUpper(uuid.NewString()[:8])
	old, rotated := "old-value-"+uuid.NewString()[:12], "new-value-"+uuid.NewString()[:12]
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", key, old, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.eng.DeleteEnvVar(context.Background(), store.ScopeSystem, "", key) })
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	run, err := h.store.CreateRun(ctx, "local", def.Name, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	h.eng.Start(run.ID)
	queuedJob(t, h.store, run.ID)
	noForm(t, "queued", map[string]string{"database": pgDump(t, h.store)}, old)

	// Rotated after the job was queued, before any worker claimed it.
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", key, rotated, true); err != nil {
		t.Fatal(err)
	}
	w := joinWorker(t, h, label)
	job, err := h.eng.Claim(ctx, w, 10*time.Second)
	if err != nil || job == nil {
		t.Fatalf("claim = %v %v", job, err)
	}
	var p JobPayload
	_ = json.Unmarshal(job.Payload, &p)
	if p.Env[key] != rotated {
		t.Fatalf("the claim did not deliver the rotated value (has %d vars)", len(p.Env))
	}
	if len(p.Deliver) == 0 {
		t.Fatal("the claim response does not name what was delivered")
	}

	// The worker echoes it in every form: as events, in its output and in its
	// error, which the platform also logs.
	forms := valueForms(rotated)
	all := strings.Join([]string{forms["raw"], forms["base64"], forms["hex"], forms["HEX"]}, " | ")
	h.eng.IngestWorkerEvents(ctx, run.ID, []WorkerEvent{{StepID: "use", Kind: "log", Payload: map[string]any{"line": all}}})
	stored, err := h.store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	res := h.eng.ScrubJobResult(ctx, run.ID, stored.Payload, JobResult{OK: false, ExitCode: 3, Stdout: all, Stderr: all, Error: "failed with " + all})
	raw, _ := json.Marshal(res)
	if err := h.store.FinishJob(ctx, job.ID, w.ID, raw); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(run.ID); r.Status != "failed" {
		t.Fatalf("run = %s", r.Status)
	}
	noForm(t, "after the end", map[string]string{"database": pgDump(t, h.store), "server log": logs.String()}, rotated, old)
}

// A job that names a value which no longer exists at claim: the claim fails
// naming the variable (never a value), the step fails with that reason, and
// the job is not handed out again.
func TestAClaimMissingADeliveredValueFailsTheStepByName(t *testing.T) {
	label := "deliver-" + uuid.NewString()[:8]
	def := &workflow.Definition{Name: "deliver-missing", Steps: []workflow.Step{{ID: "use", Run: "echo hi", RunsOn: label}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	ctx := context.Background()
	key := "DELIVER_GONE_" + strings.ToUpper(uuid.NewString()[:8])
	value := "gone-value-" + uuid.NewString()[:12]
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", key, value, true); err != nil {
		t.Fatal(err)
	}
	run, err := h.store.CreateRun(ctx, "local", def.Name, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	h.eng.Start(run.ID)
	jobID := queuedJob(t, h.store, run.ID)
	if err := h.eng.DeleteEnvVar(ctx, store.ScopeSystem, "", key); err != nil {
		t.Fatal(err)
	}
	w := joinWorker(t, h, label)
	job, err := h.eng.Claim(ctx, w, 5*time.Second)
	if job != nil || err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), value) {
		t.Fatalf("claim = %v, %v", job, err)
	}
	j, _ := h.store.GetJob(ctx, jobID)
	if j.Status != "done" || !strings.Contains(string(j.Result), key) {
		t.Fatalf("job = %s %s", j.Status, j.Result)
	}
	if again, err := h.eng.Claim(ctx, w, time.Second); again != nil || err != nil {
		t.Fatalf("the job was handed out again: %v %v", again, err)
	}
	r := h.wait(run.ID)
	if r.Status != "failed" || !strings.Contains(r.Error, key) {
		t.Fatalf("run = %s %q", r.Status, r.Error)
	}
}

// A module's token and an agent job's env-store value are delivered at claim
// the same way: in the response, registered with the run's redactor, stored
// nowhere in any form.
func TestTheModuleTokenAndAnAgentJobsValueAreDeliveredAndScrubbed(t *testing.T) {
	label := "deliver-" + uuid.NewString()[:8]
	def := &workflow.Definition{Name: "deliver-kinds", Steps: []workflow.Step{{ID: "noop", Run: "true"}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	ctx := context.Background()
	key := "DELIVER_AGENT_" + strings.ToUpper(uuid.NewString()[:8])
	value := "agent-value-" + uuid.NewString()[:12]
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", key, value, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.eng.DeleteEnvVar(context.Background(), store.ScopeSystem, "", key) })
	run, err := h.store.CreateRun(ctx, "local", def.Name, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(p JobPayload) {
		raw, _ := json.Marshal(p)
		if _, err := h.store.EnqueueJob(ctx, run.ID, p.StepID, label, raw); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(JobPayload{RunID: run.ID.String(), StepID: "mod", Command: "true", Project: "local",
		Deliver: []string{"WFX_MODULE_TOKEN"}, Module: "probe-mod"})
	enqueue(JobPayload{RunID: run.ID.String(), StepID: "agent", Kind: "agent", Project: "local",
		Agent: &AgentJob{RunID: run.ID.String(), Step: workflow.Step{ID: "agent", Env: map[string]string{"PLAIN": "x"}}, Deliver: []string{key}}})

	w := joinWorker(t, h, label)
	var token string
	for i := 0; i < 2; i++ {
		job, err := h.eng.Claim(ctx, w, 5*time.Second)
		if err != nil || job == nil {
			t.Fatalf("claim %d = %v %v", i, job, err)
		}
		var p JobPayload
		_ = json.Unmarshal(job.Payload, &p)
		switch p.StepID {
		case "mod":
			token = p.Env["WFX_MODULE_TOKEN"]
			if !strings.HasPrefix(token, "wfm_") {
				t.Fatalf("module token not delivered: %d vars", len(p.Env))
			}
		case "agent":
			if p.Agent == nil || p.Agent.Step.Env[key] != value || p.Agent.Step.Env["PLAIN"] != "x" {
				t.Fatal("the agent job's value was not delivered into its step env")
			}
		}
	}
	forms := strings.Join([]string{valueForms(token)["base64"], valueForms(value)["hex"], token, value, valueForms(value)["base64"], valueForms(token)["hex"]}, " ")
	h.eng.IngestWorkerEvents(ctx, run.ID, []WorkerEvent{{StepID: "mod", Kind: "log", Payload: forms}, {StepID: "agent", Kind: "log", Payload: forms}})
	noForm(t, "after the claims", map[string]string{"database": pgDump(t, h.store)}, token, value)
}
