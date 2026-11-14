package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The conversation module's API key, held in the env store, reaches the
// service and appears nowhere the platform keeps: not the run, not a step's
// output or prompt, not an event, not the server log.
func TestConversationKeyNeverReachesARecord(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	t.Setenv("WFX_SECRET_KEY", "dGVzdC1rZXktMzItYnl0ZXMtZXhhY3RseS0wMDAwMDA=")
	const canary = "canary-7f3e9b1c5d2a4e6f8091"
	var authorized atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+canary {
			http.Error(w, "unauthorized", 401)
			return
		}
		authorized.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "fields": map[string]any{"name": "Ada"}})
	}))
	defer srv.Close()

	mods, _ := filepath.Abs(filepath.Join("..", "..", "..", "..", "modules"))
	found, errs := workflow.FindModules("builtin", mods)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wf.yaml"), []byte(`name: conv-canary
steps:
  - id: read
    uses: conversation.task_status@0
    with: { task_id: t-1, service_url_env: CONV_URL, api_key_env: CONV_KEY }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	defs, err := workflow.LoadDirWithTasks(dir, "", nil, workflow.WithModules(found))
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, defs["conv-canary"], newFakeLLM(t), "")
	h.eng.cfg.ModulesDir = mods
	ctx := context.Background()
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", "CONV_KEY", canary, true); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", "CONV_URL", srv.URL, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.eng.DeleteEnvVar(ctx, store.ScopeSystem, "", "CONV_KEY")
		_ = h.eng.DeleteEnvVar(ctx, store.ScopeSystem, "", "CONV_URL")
	})

	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	run := h.run(nil)
	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if authorized.Load() != 1 {
		t.Fatal("the key never reached the service")
	}

	var seen []string
	add := func(what string, v any) {
		b, _ := json.Marshal(v)
		seen = append(seen, what+": "+string(b))
	}
	r, _ := h.store.GetRun(ctx, run.ID)
	add("run", r)
	steps, _ := h.store.ListSteps(ctx, run.ID)
	add("steps", steps)
	events, _ := h.store.ListEvents(ctx, run.ID, 0, 10000)
	add("events", events)
	seen = append(seen, "log: "+logs.String())
	for _, s := range seen {
		if strings.Contains(s, canary) {
			t.Fatalf("the key appears in %s", s[:strings.Index(s, ":")])
		}
	}
	if !strings.Contains(seen[1], "Ada") {
		t.Fatalf("the step output is missing the result: %s", seen[1])
	}
}
