package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// After a server restart nothing is registered in memory. A worker's live
// events and its report for a job queued before the restart are still
// scrubbed of the run's env-store values, loaded again from the store.
func TestAfterARestartAWorkersEventsAndReportAreScrubbed(t *testing.T) {
	const canary = "restart-canary-7f3e9b1c5d2a"
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cfg := config.Config{Addr: "127.0.0.1:0", WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(),
		SecretKeyEnv: "WFX_SCRUB_TEST_NO_KEY", SecretKeyPath: filepath.Join(t.TempDir(), "k")}
	before := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if err := before.SetEnvVar(ctx, store.ScopeSystem, "", "CANARY_KEY", canary, true); err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRun(ctx, "local", "remote", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	label := "restartbox-" + uuid.NewString()[:8]
	job, err := st.EnqueueJob(ctx, run.ID, "echo", label, json.RawMessage(`{"runId":"`+run.ID.String()+`","command":"env"}`))
	if err != nil {
		t.Fatal(err)
	}

	// The restart: a new engine over the same store, nothing registered.
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	f := exposeFixture{h: New(eng, st, nil, cfg.Addr, "", nil), st: st, eng: eng}
	tok, err := eng.RegistrationToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	jb, _ := json.Marshal(map[string]any{"token": tok, "name": "restart-worker", "labels": []string{label}})
	code, joined, _ := f.call(t, "POST", "/api/workers/join", jb, nil)
	if code != 200 && code != 201 {
		t.Fatalf("join = %d %v", code, joined)
	}
	auth := map[string]string{"Authorization": "Bearer " + joined["token"].(string)}
	req := httptest.NewRequest("POST", "/api/workers/claim?wait=1s", nil)
	req.Header.Set("Authorization", auth["Authorization"])
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), job.ID.String()) {
		t.Fatalf("claim = %d %s", rec.Code, rec.Body.String())
	}

	ev, _ := json.Marshal(map[string]any{"events": []map[string]any{{"stepId": "echo", "kind": "log",
		"payload": map[string]any{"stdout": "CANARY_KEY=" + canary}}}})
	if code, out, _ := f.call(t, "POST", "/api/workers/jobs/"+job.ID.String()+"/events", ev, auth); code != 200 {
		t.Fatalf("events = %d %v", code, out)
	}
	res, _ := json.Marshal(engine.JobResult{OK: true, Stdout: "CANARY_KEY=" + canary})
	if code, out, _ := f.call(t, "POST", "/api/workers/jobs/"+job.ID.String()+"/result", res, auth); code != 200 {
		t.Fatalf("result = %d %v", code, out)
	}

	evs, _ := st.ListEvents(ctx, run.ID, 0, 100)
	b, _ := json.Marshal(evs)
	stored, _ := st.GetJob(ctx, job.ID)
	for where, text := range map[string]string{"events": string(b), "job result": string(stored.Result)} {
		if strings.Contains(text, canary) || !strings.Contains(text, "[redacted:CANARY_KEY]") {
			t.Fatalf("%s: %s", where, text)
		}
	}

	// A report for a job that cannot be loaded is not stored at all.
	if code, _, _ := f.call(t, "POST", "/api/workers/jobs/"+uuid.NewString()+"/result", res, auth); code != 404 {
		t.Fatalf("a report for an unknown job = %d, want 404", code)
	}
}
