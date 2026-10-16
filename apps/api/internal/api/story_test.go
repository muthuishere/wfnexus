package api

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The run's headline verdict travels with the run, on the detail and the list.
func TestRunOutcomeIsServed(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")
	ctx := context.Background()
	run, err := st.CreateRun(ctx, "local", "watch", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunOutcome(ctx, run.ID, "incident: api 5xx"); err != nil {
		t.Fatal(err)
	}
	res := do(h, "GET", "/api/runs/"+run.ID.String(), "", nil)
	if res.Code != 200 {
		t.Fatalf("detail: %d %s", res.Code, res.Body)
	}
	var d struct {
		Run struct {
			Outcome string `json:"outcome"`
		} `json:"run"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &d)
	if d.Run.Outcome != "incident: api 5xx" {
		t.Fatalf("detail outcome = %q", d.Run.Outcome)
	}
	res = do(h, "GET", "/api/runs?workflow=watch", "", nil)
	if res.Code != 200 || !bytes.Contains(res.Body.Bytes(), []byte(`"outcome":"incident: api 5xx"`)) {
		t.Fatalf("list: %d %s", res.Code, res.Body)
	}
}

// "Next run at" comes from the workflow's own cron.
func TestWorkflowScheduleAnswersNextRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	defs := map[string]*workflow.Definition{
		"watch":  {Name: "watch", On: workflow.Triggers{Schedule: []workflow.Schedule{{Cron: "*/15 * * * *"}}}},
		"manual": {Name: "manual"},
	}
	eng := engine.New(config.Config{Addr: "127.0.0.1:8090"}, st, nil, defs, skills.Load(), nil)
	h := New(eng, st, nil, "127.0.0.1:8090", "", nil)

	var body struct {
		NextRunAt *time.Time `json:"nextRunAt"`
		Crons     []string   `json:"crons"`
	}
	res := do(h, "GET", "/api/workflows/watch/schedule", "", nil)
	if res.Code != 200 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	if body.NextRunAt == nil || body.NextRunAt.Minute()%15 != 0 || !body.NextRunAt.After(time.Now()) {
		t.Fatalf("nextRunAt = %v", body.NextRunAt)
	}
	if len(body.Crons) != 1 {
		t.Fatalf("crons = %v", body.Crons)
	}
	body.NextRunAt = nil
	res = do(h, "GET", "/api/workflows/manual/schedule", "", nil)
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	if res.Code != 200 || body.NextRunAt != nil {
		t.Fatalf("manual: %d %v", res.Code, body.NextRunAt)
	}
}
