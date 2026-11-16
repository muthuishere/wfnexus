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
	waitSecretsEnded(t, h, run.ID, true)
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

// waitSecretsEnded waits until the engine has started the run's grace period
// and, with idle, until no step of it is in flight. setRun writes the status
// first and marks the run's secrets ended after it, and a step's end restarts
// the period (stepFinished), so a test that sees the status can otherwise
// move its clock before the period has begun.
func waitSecretsEnded(t *testing.T, h *harness, id uuid.UUID, idle bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.eng.redactions.mu.Lock()
		_, ended := h.eng.redactions.ended[id]
		busy := h.eng.redactions.busy[id]
		h.eng.redactions.mu.Unlock()
		if ended && (!idle || busy == 0) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the run's grace period never started")
}

// When a step parks the run, a sibling of its wave runs on to its end. What
// that sibling writes after the park, even after the grace period, is still
// scrubbed: runOneStep keeps the run's secrets while its step is in flight.
// The park is made from outside, once the sibling is running, because inside
// a real wave the order of the two is not fixed.
func TestASiblingStillRunningAfterAParkIsScrubbed(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "go")
	sib := workflow.Step{ID: "s", Run: `while [ ! -f ` + gate + ` ]; do sleep 0.05; done; printf 'token %s\n' "$R4_TOKEN"`,
		Env: map[string]string{"R4_TOKEN": r4Secret}}
	def := &workflow.Definition{Name: "r6-sibling-" + uuid.NewString()[:8], Steps: []workflow.Step{{ID: "p", Run: "true"}, sib}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	clk := &fakeClock{t: time.Now()}
	h.eng.redactions.mu.Lock()
	h.eng.redactions.now = clk.now
	h.eng.redactions.mu.Unlock()
	ctx := context.Background()
	run, err := h.store.CreateRun(ctx, "local", def.Name, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"p", "s"} {
		if err := h.store.EnsureStep(ctx, run.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		h.eng.runOneStep(ctx, run.ID, def, &def.Steps[1], workflow.TemplateData{RunID: run.ID.String(), WorkDir: t.TempDir()})
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if s := h.steps(run.ID)["s"]; s != nil && s.Status == "running" && len(h.eng.redactions.get(run.ID)) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sibling never started")
		}
	}
	h.eng.setRun(ctx, run.ID, "awaiting_approval", "p", "") // its sibling parks the run
	clk.add(endedSecretsGrace + time.Minute)
	h.eng.redactions.sweep()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("the sibling never finished")
	}
	stored := string(mustJSON(h.steps(run.ID)["s"])) + revEventsR5(t, h, run.ID)
	if r4Clean(t, "sibling after a park", stored); !strings.Contains(stored, "[redacted:R4_TOKEN]") {
		t.Fatalf("the sibling's output: %s", stored)
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

// An ACP agent's turn error quotes what the agent did, shortened: a refused
// tool's title (80 bytes), the stderr tail of an agent that died (4 KB). The
// run's Scrub reaches the adapter on every turn and runs on the whole text
// before the cut, so a value the cut splits leaves no piece in the step
// error, the run error or the server log.
func TestAnACPAgentsShortenedErrorKeepsNoPieceOfASecret(t *testing.T) {
	for name, act := range map[string]string{
		// 84 bytes: cut at 80, 20 bytes into the value; scrubbed, it fits
		"refused title": `title="."*60+os.environ["R4_TOKEN"]`,
		// 4100 bytes: the last 4 KB start 4 bytes into the value; scrubbed, it fits
		"stderr tail": `sys.stderr.write(os.environ["R4_TOKEN"]+"#"*4075+"\n"); sys.stderr.flush(); sys.exit(3)`,
	} {
		t.Run(name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "agent.py")
			src := `#!/usr/bin/env python3
import json,os,sys
def send(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
pending=None
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    m=json.loads(line)
    method=m.get("method"); mid=m.get("id")
    if method=="initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1}})
    elif method=="session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"s"}})
    elif method=="session/prompt":
        ` + act + `
        pending=mid
        send({"jsonrpc":"2.0","id":9001,"method":"session/request_permission","params":{"sessionId":"s",
              "toolCall":{"toolCallId":"c1","title":title,"kind":"read"},
              "options":[{"optionId":"no","kind":"reject_once","name":"Reject"}]}})
    elif mid==9001 and pending is not None:
        send({"jsonrpc":"2.0","id":pending,"result":{"stopReason":"end_turn"}})
        pending=None
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`
			if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
				t.Fatal(err)
			}
			def := &workflow.Definition{Name: "r6-acp-" + uuid.NewString()[:8], Steps: []workflow.Step{
				{ID: "a", Prompt: "go", Provider: "r6-acp", Env: map[string]string{"R4_TOKEN": r4Secret}},
			}}
			normalizeForTest(def)
			h := newHarness(t, def, newFakeLLM(t), "")
			h.eng.Catalog().Providers.Add(catalog.Provider{Name: "r6-acp", Kind: catalog.KindACP,
				Command: []string{script}, Repairs: -1}, "test")
			var logs bytes.Buffer
			log.SetOutput(&logs)
			defer log.SetOutput(os.Stderr)
			run := h.run(nil)
			for where, text := range map[string]string{
				"step error": h.steps(run.ID)["a"].Error, "run error": run.Error, "log": logs.String(),
			} {
				r4Clean(t, where, text)
			}
			if run.Status != "failed" || !strings.Contains(run.Error, "[redacted:R4_TOKEN]") {
				t.Fatalf("run = %s: %s", run.Status, run.Error)
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

// Values registered again for a worker's late event on a run that is parked
// in the store get the grace period too: they do not stay in memory for good.
func TestValuesRegisteredForAParkedRunAreDropped(t *testing.T) {
	h, id := r4Run(t)
	clk := &fakeClock{t: time.Now()}
	h.eng.redactions.mu.Lock()
	h.eng.redactions.now = clk.now
	h.eng.redactions.mu.Unlock()
	ctx := context.Background()
	h.eng.setRun(ctx, id, "awaiting_approval", "s", "")
	clk.add(endedSecretsGrace + time.Second)
	h.eng.redactions.sweep()
	payload, _ := json.Marshal(JobPayload{Env: map[string]string{"R4_TOKEN": r4Secret}})
	h.eng.IngestWorkerEvents(ctx, id, payload, []WorkerEvent{{StepID: "s", Payload: map[string]any{"text": "late " + r4Secret}}})
	if r4Clean(t, "late event", eventTexts(t, h.store, id)); len(h.eng.redactions.get(id)) == 0 {
		t.Fatal("not registered again for the late event")
	}
	clk.add(endedSecretsGrace + time.Second)
	h.eng.redactions.sweep()
	if len(h.eng.redactions.get(id)) != 0 {
		t.Fatal("values registered for a parked run stay in memory")
	}
}
