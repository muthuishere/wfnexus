package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A run parked on a person (needs_input, awaiting_approval) can wait for
// days. Its secrets are held like an ended run's: a grace period, then
// dropped, under the same cap; many parked runs stay bounded.
func TestParkedRunsSecretsAreBounded(t *testing.T) {
	h, _ := r4Run(t)
	clk := &fakeClock{t: time.Now()}
	h.eng.redactions.mu.Lock()
	h.eng.redactions.now = clk.now
	h.eng.redactions.mu.Unlock()
	ctx := context.Background()
	const n = 2 * maxEndedSecrets
	for i := 0; i < n; i++ {
		run, err := h.store.CreateRun(ctx, "local", h.currentWorkflow(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		h.eng.redactions.add(run.ID, map[string]string{"P_TOKEN": fmt.Sprintf("parked-value-%06d", i)}, nil)
		status := "needs_input"
		if i%2 == 1 {
			status = "awaiting_approval"
		}
		h.eng.setRun(ctx, run.ID, status, "s", "")
		if held := h.eng.redactions.held(); held > maxEndedSecrets+1 { // +1: r4Run's own run, still running
			t.Fatalf("after %d parked runs %d sets are held, more than the cap %d", i+1, held, maxEndedSecrets)
		}
	}
	clk.add(endedSecretsGrace)
	h.eng.redactions.sweep()
	if held := h.eng.redactions.held(); held != 1 {
		t.Fatalf("a grace period after they parked, %d sets are held; want only the running one", held)
	}
}

// A parked run's set is dropped while it waits; when it resumes, its next step
// registers its values again before it runs, so its output is still scrubbed.
func TestAResumedRunIsScrubbedAfterItsSetWasDropped(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	def := &workflow.Definition{Name: "r5-resume-" + uuid.NewString()[:8], Steps: []workflow.Step{
		{ID: "a", Run: `echo started`, Env: map[string]string{"R4_TOKEN": r4Secret}},
		{ID: "b", Run: `printf 'token %s\n' "$R4_TOKEN"; printf '%s' "$R4_TOKEN" > ` + out,
			Env: map[string]string{"R4_TOKEN": r4Secret}, RequiresApproval: true},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	clk := &fakeClock{t: time.Now()}
	h.eng.redactions.mu.Lock()
	h.eng.redactions.now = clk.now
	h.eng.redactions.mu.Unlock()
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("run = %s: %s", run.Status, run.Error)
	}
	clk.add(endedSecretsGrace)
	h.eng.redactions.sweep()
	if got := h.eng.redactions.get(run.ID); len(got) != 0 {
		t.Fatalf("the parked run still holds %d secrets after the grace period", len(got))
	}
	if err := h.eng.Approve(context.Background(), run.ID, "b", Actor{ID: "anyone", Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	if run = h.wait(run.ID); run.Status != "done" {
		t.Fatalf("run = %s: %s", run.Status, run.Error)
	}
	if b, _ := os.ReadFile(out); string(b) != r4Secret {
		t.Fatalf("the step did not see its value: %q", b)
	}
	stored := string(mustJSON(h.steps(run.ID)["b"])) + revEventsR5(t, h, run.ID)
	if r4Clean(t, "resumed step", stored); !strings.Contains(stored, "[redacted:R4_TOKEN]") {
		t.Fatalf("the resumed step's output: %s", stored)
	}
}

func revEventsR5(t *testing.T, h *harness, id uuid.UUID) string {
	t.Helper()
	evs, err := h.store.ListEvents(context.Background(), id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return string(mustJSON(evs))
}

// A local agent CLI whose reply cannot be read fails its step with the reply
// quoted, shortened to its two ends (agentadapter). The reply is scrubbed
// whole before that shortening, so a value cut in two by it leaves no piece
// in the step error, the run error or the server log.
func TestARejectedAgentReplyKeepsNoPieceOfASecret(t *testing.T) {
	for name, reply := range map[string]string{
		// strings.Fields joins the reply; the head keeps its first 300 bytes.
		"head cut": strings.Repeat("a ", 140) + r4Secret + strings.Repeat(" b", 400),
		// the tail keeps its last 300 bytes.
		"tail cut": strings.Repeat("a ", 400) + r4Secret + strings.Repeat(" b", 145),
	} {
		t.Run(name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "agent.sh")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '"+reply+"'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			def := &workflow.Definition{Name: "r5-reply-" + uuid.NewString()[:8], Steps: []workflow.Step{
				{ID: "a", Prompt: "go", Provider: "r5-reply-cli", Env: map[string]string{"R4_TOKEN": r4Secret}},
			}}
			normalizeForTest(def)
			h := newHarness(t, def, newFakeLLM(t), "")
			h.eng.Catalog().Providers.Add(catalog.Provider{Name: "r5-reply-cli", Kind: catalog.KindCLI,
				Command: []string{script}, Repairs: -1}, "test")
			var logs bytes.Buffer
			log.SetOutput(&logs)
			defer log.SetOutput(os.Stderr)
			run := h.run(nil)
			if run.Status != "failed" || !strings.Contains(run.Error, "last reply") {
				t.Fatalf("run = %s: %s", run.Status, run.Error)
			}
			for where, text := range map[string]string{
				"step error": h.steps(run.ID)["a"].Error, "run error": run.Error, "log": logs.String(),
			} {
				r4Clean(t, where, text)
			}
		})
	}
}

// runAgent's "step failed" text is scrubbed of the run's values BEFORE the
// identifier patterns run: a value that holds `user_id=` would otherwise be
// half taken by the pattern, and the half left no longer matches the value.
func TestAFailedAgentStepsTextIsScrubbedBeforeIdentifiers(t *testing.T) {
	const shaped = "zz9Kq7Wv_user_id=Wx3Lm9Pt"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request for ` + shaped + ` end"}}`))
	}))
	t.Cleanup(srv.Close)
	def := &workflow.Definition{Name: "r5-failed-" + uuid.NewString()[:8], Steps: []workflow.Step{
		{ID: "a", Prompt: "go", Env: map[string]string{"SHAPED_TOKEN": shaped}},
	}}
	normalizeForTest(def)
	h := newHarness(t, def, &fakeLLM{Server: srv}, "")
	run := h.run(nil)
	if run.Status != "failed" {
		t.Fatalf("run = %s: %s", run.Status, run.Error)
	}
	stored := run.Error + h.steps(run.ID)["a"].Error + revEventsR5(t, h, run.ID)
	for _, piece := range []string{"zz9Kq7", "Wx3Lm9"} {
		if strings.Contains(stored, piece) {
			t.Fatalf("%q of the value is kept: %s", piece, stored)
		}
	}
	if !strings.Contains(run.Error, "[redacted:SHAPED_TOKEN]") {
		t.Fatalf("run error: %s", run.Error)
	}
}

// memoryTool writes MEMORY.md, with no step row or event behind it: the note
// is scrubbed there or nowhere.
func TestAMemoryNoteIsScrubbed(t *testing.T) {
	h, id := r4Run(t)
	run, _ := h.store.GetRun(context.Background(), id)
	tools := h.eng.memoryTool(context.Background(), id, run.Workflow, &workflow.Step{ID: "s"})
	if len(tools) == 0 {
		t.Fatal("no memory tool")
	}
	res, err := tools[0].Execute(map[string]any{"action": "add", "text": "remember " + r4Secret}, &tn.ToolContext{})
	if err != nil || res.IsError {
		t.Fatalf("%v %v", err, res)
	}
	dir, _ := h.eng.MemoryDir(run.Project, run.Workflow)
	t.Cleanup(func() { os.RemoveAll(dir) })
	b, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if !strings.Contains(string(b), "remember [redacted:R4_TOKEN]") {
		t.Fatalf("MEMORY.md: %q", b)
	}
	r4Clean(t, "MEMORY.md", string(b))
}
