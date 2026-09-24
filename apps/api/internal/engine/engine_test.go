package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// These tests run the real engine against a scripted LLM. They need the local
// Postgres + MinIO (task infra:up); without them the suite skips rather than
// pretending to pass.
func testDeps(t *testing.T) (*store.Store, *blob.Blob, config.Config) {
	t.Helper()
	cfg := config.Load()
	// These are the POSTGRES path. The no-config default is now `local`
	// (sqlite in a file), so postgres is STATED here rather than inherited from
	// a default that has moved — otherwise this would quietly exercise a
	// different database and skip the one it means to test.
	if cfg.StorageDriver != "postgres" {
		cfg.StorageDriver = "postgres"
		cfg.DatabaseURL = "postgres://bfp:bfp@127.0.0.1:5460/bfp?sslmode=disable"
	}
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	ctx := context.Background()
	if err := store.Migrate(cfg.StorageDriver, cfg.DatabaseURL); err != nil {
		t.Skipf("no postgres (task infra:up): %v", err)
	}
	st, err := store.Open(ctx, cfg.StorageDriver, cfg.DatabaseURL)
	if err != nil {
		t.Skipf("no postgres: %v", err)
	}
	t.Cleanup(st.Close)
	bl, err := blob.Open(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	if err != nil {
		t.Skipf("no minio: %v", err)
	}
	return st, bl, cfg
}

type harness struct {
	eng   *Engine
	store *store.Store
	llm   *fakeLLM
	t     *testing.T
	runs  []uuid.UUID
}

// newHarness builds an engine whose LLM is the scripted server.
func newHarness(t *testing.T, def *workflow.Definition, llm *fakeLLM, skillRoot string) *harness {
	t.Helper()
	st, bl, cfg := testDeps(t)
	cfg.LLMBaseURL = llm.URL
	cfg.LLMStyle = "openai"
	cfg.Model = "fake"
	cfg.WorkDir = t.TempDir()
	if skillRoot == "" {
		skillRoot = t.TempDir()
	}
	reg := skills.Load(skillRoot)
	cat, _ := catalog.Load("", "")
	eng := New(cfg, st, bl, map[string]*workflow.Definition{def.Name: def}, reg, cat)
	h := &harness{eng: eng, store: st, llm: llm, t: t}
	// stop anything still running before the connection pool closes
	t.Cleanup(func() {
		for _, id := range h.runs {
			eng.Cancel(context.Background(), id)
		}
		time.Sleep(50 * time.Millisecond)
	})
	return h
}

// run starts a run and waits for it to leave the running states.
func (h *harness) run(input map[string]any) *store.Run {
	h.t.Helper()
	raw, _ := json.Marshal(input)
	run, err := h.store.CreateRun(context.Background(), "local", h.currentWorkflow(), raw)
	if err != nil {
		h.t.Fatal(err)
	}
	h.runs = append(h.runs, run.ID)
	h.eng.Start(run.ID)
	return h.wait(run.ID)
}

func (h *harness) currentWorkflow() string {
	for name := range h.eng.Definitions() {
		return name
	}
	return ""
}

// wait polls until the run reaches a state that needs someone else to act.
func (h *harness) wait(id uuid.UUID) *store.Run {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		run, err := h.store.GetRun(context.Background(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		switch run.Status {
		case "done", "failed", "cancelled", "awaiting_approval", "needs_input":
			return run
		}
		time.Sleep(50 * time.Millisecond)
	}
	run, _ := h.store.GetRun(context.Background(), id)
	h.t.Fatalf("run stuck in %q at step %q", run.Status, run.CurrentStep)
	return nil
}

func (h *harness) steps(id uuid.UUID) map[string]*store.StepRun {
	h.t.Helper()
	list, err := h.store.ListSteps(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]*store.StepRun{}
	for _, s := range list {
		out[s.StepID] = s
	}
	return out
}

func output(t *testing.T, s *store.StepRun) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(s.Output, &m); err != nil {
		t.Fatalf("step %s output %q: %v", s.StepID, s.Output, err)
	}
	return m
}

// objSchema is a small helper for required-string-and-bool schemas.
func objSchema(required []any, props map[string]any) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": required, "properties": props,
	}
}

func strProp() map[string]any  { return map[string]any{"type": "string"} }
func boolProp() map[string]any { return map[string]any{"type": "boolean"} }

// ---------------------------------------------------------------------------

// The core promise: a step is an AGENT LOOP, not one prompt. The model calls a
// tool, sees its result, then submits — and the next step receives the typed
// output, not prose.
func TestStepRunsAnAgentLoopAndHandsOffTypedOutput(t *testing.T) {
	def := &workflow.Definition{
		Name: "handoff",
		Steps: []workflow.Step{
			{
				ID: "find", Prompt: "find the bug", Tools: []string{"bash"}, MaxTurns: 6, MaxAttempts: 2,
				OutputSchema: objSchema([]any{"component", "found"},
					map[string]any{"component": strProp(), "found": boolProp()}),
			},
			{
				ID: "fix", Prompt: "fix {{ .Steps.find.component }} (found={{ .Steps.find.found }})",
				Tools: []string{"bash"}, MaxTurns: 6, MaxAttempts: 2,
				OutputSchema: objSchema([]any{"patched"}, map[string]any{"patched": boolProp()}),
			},
		},
	}
	normalizeForTest(def)

	llm := newFakeLLM(t,
		// step 1: two tool turns then a submission — a real loop
		bashCall("echo scanning"),
		bashCall("echo cart.py"),
		submit(map[string]any{"component": "cart.py", "found": true}),
		finish(),
		// step 2
		submit(map[string]any{"patched": true}),
		finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(map[string]any{"title": "t"})

	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	steps := h.steps(run.ID)
	if got := output(t, steps["find"])["component"]; got != "cart.py" {
		t.Fatalf("step 1 output = %v", got)
	}
	if steps["find"].Turns < 3 {
		t.Fatalf("step 1 took %d turns — the loop did not run", steps["find"].Turns)
	}
	if got := output(t, steps["fix"])["patched"]; got != true {
		t.Fatalf("step 2 output = %v", got)
	}
	// the typed hand-off must be visible in step 2's prompt
	if p := steps["fix"].Prompt; !strings.Contains(p, "fix cart.py (found=true)") {
		t.Fatalf("step 2 prompt did not receive the typed output: %q", p)
	}
	// and every step got only its allowlisted tool plus the submission tool
	offered := llm.toolNamesOffered(0)
	if !contains2(offered, "bash") || !contains2(offered, "submit_output") {
		t.Fatalf("tools offered = %v", offered)
	}
	for _, denied := range []string{"write", "edit", "apply_patch", "webfetch"} {
		if contains2(offered, denied) {
			t.Fatalf("step was offered %q which its YAML did not grant: %v", denied, offered)
		}
	}
}

// Validation happens INSIDE the loop: a bad submission comes back as the tool
// result and the model corrects itself, rather than the step dying.
func TestInvalidOutputIsFedBackAndTheModelCorrects(t *testing.T) {
	def := &workflow.Definition{
		Name: "selfcorrect",
		Steps: []workflow.Step{{
			ID: "check", Prompt: "check", Tools: []string{"bash"}, MaxTurns: 8, MaxAttempts: 2,
			OutputSchema: objSchema([]any{"verdict", "ok"}, map[string]any{
				"verdict": map[string]any{"type": "string", "enum": []any{"pass", "fail"}},
				"ok":      boolProp(),
			}),
		}},
	}
	normalizeForTest(def)

	llm := newFakeLLM(t,
		submit(map[string]any{"verdict": "maybe", "ok": "yes"}), // wrong enum AND wrong type
		submit(map[string]any{"verdict": "pass", "ok": true}),   // corrected
		finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	if got := output(t, h.steps(run.ID)["check"])["verdict"]; got != "pass" {
		t.Fatalf("stored output = %v, want the corrected one", got)
	}
	if llm.calls() < 2 {
		t.Fatalf("model was not asked again after the invalid submission (%d calls)", llm.calls())
	}
}

// A step that never submits must fail LOUDLY after its attempts, never record a
// silent success.
func TestStepWithoutSubmissionFailsLoudly(t *testing.T) {
	def := &workflow.Definition{
		Name: "nosubmit",
		Steps: []workflow.Step{{
			ID: "drift", Prompt: "do something", Tools: []string{"bash"}, MaxTurns: 2, MaxAttempts: 1,
			OutputSchema: objSchema([]any{"done"}, map[string]any{"done": boolProp()}),
		}},
	}
	normalizeForTest(def)

	llm := newFakeLLM(t, turn{text: "I had a nice think about it."})
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if !strings.Contains(run.Error, "drift") {
		t.Fatalf("error does not name the step: %q", run.Error)
	}
	st := h.steps(run.ID)["drift"]
	if st.Status != "failed" || st.Output != nil {
		t.Fatalf("step = %s output=%s", st.Status, st.Output)
	}
}

func contains2(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// normalizeForTest applies the same defaults LoadDir would.
func normalizeForTest(d *workflow.Definition) {
	raw, _ := json.Marshal(d)
	_ = raw
	for i := range d.Steps {
		if d.Steps[i].Name == "" {
			d.Steps[i].Name = d.Steps[i].ID
		}
		if d.Steps[i].MaxTurns == 0 {
			d.Steps[i].MaxTurns = 10
		}
		if d.Steps[i].MaxAttempts == 0 {
			d.Steps[i].MaxAttempts = 2
		}
	}
	if d.InputSchema == nil {
		d.InputSchema = map[string]any{"type": "object"}
	}
}

var _ = filepath.Join

// The concurrency cap bounds machine load, not accepted work: a run beyond the
// limit waits in queued rather than being refused.
func TestConcurrentRunsAreBoundedByTheSlotLimit(t *testing.T) {
	def := &workflow.Definition{Name: "concurrency", Steps: []workflow.Step{{
		ID: "only", Prompt: "p", Tools: []string{"bash"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)

	// one slot, so the second run cannot start until the first finishes
	gate := make(chan struct{})
	var mu sync.Mutex
	inFlight, maxSeen := 0, 0
	llm := newFakeLLMFunc(t, func() turn {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		mu.Unlock()
		<-gate // hold the step open so overlap would be visible
		mu.Lock()
		inFlight--
		mu.Unlock()
		return submit(map[string]any{"ok": true})
	})

	h := newHarness(t, def, llm, "")
	h.eng.slots = make(chan struct{}, 1)

	ids := make([]uuid.UUID, 0, 3)
	for i := 0; i < 3; i++ {
		run, err := h.store.CreateRun(context.Background(), "local", "concurrency", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		h.runs = append(h.runs, run.ID)
		ids = append(ids, run.ID)
		h.eng.Start(run.ID)
	}

	// let everything through
	time.Sleep(200 * time.Millisecond)
	close(gate)
	for _, id := range ids {
		if run := h.wait(id); run.Status != "done" {
			t.Fatalf("run %s = %s (%s)", id, run.Status, run.Error)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if maxSeen > 1 {
		t.Fatalf("slot limit is 1 but %d runs executed at once", maxSeen)
	}
}
