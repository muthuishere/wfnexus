package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// ADR 0026 decisions 4 and 5, against the real engine and store. Every step
// here is a `run:` or a `start:`, so no model is involved.

func yamlDef(t *testing.T, body string) *workflow.Definition {
	t.Helper()
	d, err := workflow.ParseYAML([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// startHarness builds an engine holding every definition given.
func startHarness(t *testing.T, defs ...*workflow.Definition) *harness {
	t.Helper()
	h := newHarness(t, defs[0], newFakeLLM(t), "")
	for _, d := range defs[1:] {
		h.eng.defs[d.Name] = d
	}
	return h
}

func uniq(prefix string) string { return prefix + "-" + uuid.NewString()[:8] }

// waitEnded polls a run until it is done, failed or cancelled.
func (h *harness) waitEnded(id uuid.UUID) *store.Run {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		run, err := h.store.GetRun(context.Background(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		if runStatusEnded[run.Status] {
			return run
		}
		time.Sleep(30 * time.Millisecond)
	}
	run, _ := h.store.GetRun(context.Background(), id)
	h.t.Fatalf("run %s stuck in %q at %q", id, run.Status, run.CurrentStep)
	return nil
}

func (h *harness) start(name string, input map[string]any) uuid.UUID {
	h.t.Helper()
	raw, _ := json.Marshal(input)
	run, err := h.store.CreateRun(context.Background(), "local", name, raw)
	if err != nil {
		h.t.Fatal(err)
	}
	h.runs = append(h.runs, run.ID)
	h.eng.Start(run.ID)
	return run.ID
}

func stepOut(t *testing.T, h *harness, runID uuid.UUID, stepID string) map[string]any {
	t.Helper()
	st := h.steps(runID)[stepID]
	if st == nil {
		t.Fatalf("no step %s", stepID)
	}
	var out map[string]any
	_ = json.Unmarshal(st.Output, &out)
	return out
}

func TestStartWaitPausesTheParentAndReturnsTheChildOutput(t *testing.T) {
	child, parent := uniq("child"), uniq("parent")
	h := startHarness(t,
		yamlDef(t, "name: "+parent+`
steps:
  - id: call
    start: {workflow: `+child+`, wait: true, input: {who: "{{ .Input.who }}"}}
  - id: after
    run: echo "got {{ .Steps.call.output.stdout }}"
`),
		yamlDef(t, "name: "+child+`
steps:
  - id: greet
    run: sleep 0.3; echo hello-{{ .Input.who }}
`))
	id := h.start(parent, map[string]any{"who": "ana"})
	run := h.waitEnded(id)
	if run.Status != "done" {
		t.Fatalf("parent %s: %s", run.Status, run.Error)
	}
	out := stepOut(t, h, id, "call")
	if out["status"] != "done" || !strings.Contains(asString(out["output"]), "hello-ana") {
		t.Fatalf("call output %v", out)
	}
	if after := stepOut(t, h, id, "after"); !strings.Contains(asString(after["stdout"]), "hello-ana") {
		t.Fatalf("the next step did not see the child's output: %v", after)
	}
	kids, _ := h.store.ChildRuns(context.Background(), id)
	if len(kids) != 1 || kids[0].ParentStepID != "call" || kids[0].ParentMode != "wait" || kids[0].TriggeredBy != "run:"+id.String() {
		t.Fatalf("children %+v", kids)
	}
}

func asString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestStartWaitFailsWhenTheChildFails(t *testing.T) {
	child, parent := uniq("child"), uniq("parent")
	h := startHarness(t,
		yamlDef(t, "name: "+parent+"\nsteps:\n  - id: call\n    start: {workflow: "+child+", wait: true}\n"),
		yamlDef(t, "name: "+child+"\nsteps:\n  - id: boom\n    run: exit 3\n"))
	run := h.waitEnded(h.start(parent, nil))
	if run.Status != "failed" || !strings.Contains(run.Error, "child run") {
		t.Fatalf("parent %s: %s", run.Status, run.Error)
	}
}

func TestStartNoWaitMovesOnAndAListFansOut(t *testing.T) {
	a, b, parent := uniq("a"), uniq("b"), uniq("parent")
	h := startHarness(t,
		yamlDef(t, "name: "+parent+`
steps:
  - id: fire
    start: {workflow: `+a+`}
  - id: both
    start:
      - {workflow: `+a+`, wait: true}
      - {workflow: `+b+`, wait: true}
`),
		yamlDef(t, "name: "+a+"\nsteps:\n  - id: s\n    run: echo A\n"),
		yamlDef(t, "name: "+b+"\nsteps:\n  - id: s\n    run: echo B\n"))
	id := h.start(parent, nil)
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("parent %s: %s", run.Status, run.Error)
	}
	fire := stepOut(t, h, id, "fire")
	if fire["runId"] == "" || fire["workflow"] != a {
		t.Fatalf("fire output %v", fire)
	}
	both := stepOut(t, h, id, "both")
	runs, _ := both["runs"].([]any)
	if both["status"] != "done" || len(runs) != 2 {
		t.Fatalf("both output %v", both)
	}
	kids, _ := h.store.ChildRuns(context.Background(), id)
	if len(kids) != 3 {
		t.Fatalf("want 3 children, got %d", len(kids))
	}
}

// A workflow that starts itself nests until the depth cap stops it.
func TestStartDepthIsCapped(t *testing.T) {
	name := uniq("deep")
	h := startHarness(t, yamlDef(t, "name: "+name+"\nsteps:\n  - id: again\n    start: {workflow: "+name+", wait: true}\n"))
	top := h.waitEnded(h.start(name, nil))
	if top.Status != "failed" {
		t.Fatalf("top %s: %s", top.Status, top.Error)
	}
	runs, _ := h.store.FindRuns(context.Background(), store.RunFilter{Workflow: name})
	if len(runs) != workflow.MaxStartDepth {
		t.Fatalf("want %d nested runs, got %d", workflow.MaxStartDepth, len(runs))
	}
	var deepest *store.Run
	for _, r := range runs {
		h.runs = append(h.runs, r.ID)
		if strings.Contains(r.Error, "levels deep") {
			deepest = r
		}
	}
	if deepest == nil {
		t.Fatal("no run says why it stopped")
	}
}

func TestStartContinueEventFinishesNowAndDeliversTheEvent(t *testing.T) {
	child, parent := uniq("child"), uniq("parent")
	h := startHarness(t,
		yamlDef(t, "name: "+parent+`
steps:
  - id: later
    start: {workflow: `+child+`, continue: event}
  - id: never
    run: echo should-not-run
`),
		yamlDef(t, "name: "+child+"\nsteps:\n  - id: s\n    run: sleep 0.2; echo finished\n"))
	var mu sync.Mutex
	var got []map[string]any
	h.eng.UseEventSink(func(_ context.Context, ev map[string]any) bool {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		return true
	})
	id := h.start(parent, map[string]any{"message": map[string]any{"channel": "wa", "chat": "g1@g.us", "text": "hi"}})
	run := h.waitEnded(id)
	if run.Status != "done" {
		t.Fatalf("parent %s: %s", run.Status, run.Error)
	}
	if st := h.steps(id)["never"]; st.Status != "skipped" {
		t.Fatalf("step after continue: %s", st.Status)
	}
	for i := 0; i < 200; i++ {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want one workflow.completed, got %d", len(got))
	}
	ev := got[0]
	if ev["type"] != "workflow.completed" || ev["conversation"] != "wa:g1@g.us" || ev["chat"] != "g1@g.us" ||
		ev["parentRunId"] != id.String() || ev["status"] != "done" || !strings.Contains(asString(ev["output"]), "finished") {
		t.Fatalf("event %v", ev)
	}
}

// ---- conversations ----

func convDef(t *testing.T, name, run string) *workflow.Definition {
	return yamlDef(t, "name: "+name+"\nsteps:\n  - id: s\n    run: "+run+"\n")
}

func waitRuns(t *testing.T, h *harness, f store.RunFilter, n int) []*store.Run {
	t.Helper()
	var runs []*store.Run
	for i := 0; i < 400; i++ {
		runs, _ = h.store.FindRuns(context.Background(), f)
		if len(runs) >= n {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, r := range runs {
		h.runs = append(h.runs, r.ID)
	}
	return runs
}

func msg(ch, chat, text string) map[string]any {
	return map[string]any{"message": map[string]any{"channel": ch, "chat": chat, "text": text, "from": "u1"}}
}

func TestMessagesWithinTheWindowBatchIntoOneRun(t *testing.T) {
	name := uniq("conv")
	h := startHarness(t, convDef(t, name, "echo {{ len .Input.messages }}"))
	h.eng.SetConversationDebounce(150 * time.Millisecond)
	key := ConversationKey{Channel: "wa", Chat: uniq("chat")}
	for _, m := range []string{"one", "two", "three"} {
		if err := h.eng.StartForConversation(context.Background(), key, name, msg("wa", key.Chat, m)); err != nil {
			t.Fatal(err)
		}
	}
	runs := waitRuns(t, h, store.RunFilter{Workflow: name}, 1)
	time.Sleep(300 * time.Millisecond) // a second run, if any, would exist by now
	runs = waitRuns(t, h, store.RunFilter{Workflow: name}, 1)
	if len(runs) != 1 {
		t.Fatalf("want one batched run, got %d", len(runs))
	}
	r := h.waitEnded(runs[0].ID)
	if r.Conversation != key.String() || r.TriggeredBy != "u1" {
		t.Fatalf("run conversation %q by %q", r.Conversation, r.TriggeredBy)
	}
	var in map[string]any
	_ = json.Unmarshal(r.Input, &in)
	if msgs, _ := in["messages"].([]any); len(msgs) != 3 || asString(in["message"]) != asString(msgs[2]) {
		t.Fatalf("input %s", r.Input)
	}
	c, err := h.store.GetConversation(context.Background(), key.Channel, key.Chat)
	if err != nil || c.State != "resolved" || c.LastRunID != r.ID.String() {
		t.Fatalf("conversation %+v %v", c, err)
	}
}

func TestOneConversationRunsOneAtATimeOthersInParallel(t *testing.T) {
	name := uniq("lane")
	h := startHarness(t, convDef(t, name, "sleep 0.6"))
	h.eng.slots = make(chan struct{}, 4)
	h.eng.SetConversationDebounce(20 * time.Millisecond)
	ctx := context.Background()
	a := ConversationKey{Channel: "wa", Chat: uniq("a")}
	b := ConversationKey{Channel: "wa", Chat: uniq("b")}
	_ = h.eng.StartForConversation(ctx, a, name, msg("wa", a.Chat, "first"))
	time.Sleep(150 * time.Millisecond) // past the window: a separate batch
	_ = h.eng.StartForConversation(ctx, a, name, msg("wa", a.Chat, "second"))
	_ = h.eng.StartForConversation(ctx, b, name, msg("wa", b.Chat, "other chat"))

	// While a's first run is still going, b's has started and a's second has not.
	bRuns := waitRuns(t, h, store.RunFilter{Conversation: b.String()}, 1)
	aRuns, _ := h.store.FindRuns(ctx, store.RunFilter{Conversation: a.String()})
	if len(bRuns) != 1 || len(aRuns) != 1 {
		t.Fatalf("while a's first runs: a has %d runs, b has %d", len(aRuns), len(bRuns))
	}
	first := h.waitEnded(aRuns[0].ID)
	aRuns = waitRuns(t, h, store.RunFilter{Conversation: a.String()}, 2)
	if len(aRuns) != 2 {
		t.Fatalf("a's second message never ran: %d", len(aRuns))
	}
	second := aRuns[0] // newest first
	if second.CreatedAt.Before(first.UpdatedAt.Add(-50 * time.Millisecond)) {
		t.Fatalf("a's second run was created at %v, before the first ended at %v", second.CreatedAt, first.UpdatedAt)
	}
	h.waitEnded(second.ID)
	h.waitEnded(bRuns[0].ID)
}

func TestMemoryIsPerConversationLessonsAreNot(t *testing.T) {
	name := uniq("cmem")
	def := convDef(t, name, "echo hi")
	h := startHarness(t, def)
	forgetAfter(t, h, name)
	ctx := context.Background()
	ra := h.waitEnded(h.start(name, msg("wa", "chat-a", "hello a")))
	rb := h.waitEnded(h.start(name, msg("wa", "chat-b", "hello b")))
	if _, err := h.store.AddMemory(ctx, model.MemoryEntry{Project: "local", Workflow: name, Kind: model.MemoryKindNote, Body: "lesson-everyone-sees"}); err != nil {
		t.Fatal(err)
	}
	memA := h.eng.memoryForConv(ctx, def, "s", "local", "wa:chat-a")
	if !strings.Contains(memA, shortID(ra.ID.String())) || strings.Contains(memA, shortID(rb.ID.String())) {
		t.Fatalf("chat a's memory:\n%s", memA)
	}
	if !strings.Contains(memA, "lesson-everyone-sees") {
		t.Fatalf("a workflow-wide lesson was not loaded in a conversation:\n%s", memA)
	}
	// `memory: {scope: workflow}` keeps one memory for every chat.
	def.Memory = &workflow.Memory{Scope: workflow.MemoryScopeWorkflow}
	rc := h.waitEnded(h.start(name, msg("wa", "chat-c", "hello c")))
	all := h.eng.memoryForConv(ctx, def, "s", "local", "wa:chat-a")
	if !strings.Contains(all, shortID(rc.ID.String())) || strings.Contains(all, shortID(ra.ID.String())) {
		t.Fatalf("scope: workflow memory:\n%s", all)
	}
}

func TestAClosedConversationReopensOnANewMessage(t *testing.T) {
	name := uniq("reopen")
	h := startHarness(t, convDef(t, name, "echo ok"))
	h.eng.SetConversationDebounce(10 * time.Millisecond)
	ctx := context.Background()
	key := ConversationKey{Channel: "wa", Chat: uniq("c")}
	_ = h.eng.StartForConversation(ctx, key, name, msg("wa", key.Chat, "one"))
	h.waitEnded(waitRuns(t, h, store.RunFilter{Conversation: key.String()}, 1)[0].ID)
	if err := h.eng.SetConversationState(ctx, key, "closed"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.SetConversationState(ctx, key, "gone"); err == nil {
		t.Fatal("a made-up state was accepted")
	}
	_ = h.eng.StartForConversation(ctx, key, name, msg("wa", key.Chat, "two"))
	runs := waitRuns(t, h, store.RunFilter{Conversation: key.String()}, 2)
	if len(runs) != 2 {
		t.Fatalf("runs %d", len(runs))
	}
	c, _ := h.store.GetConversation(ctx, key.Channel, key.Chat)
	if c.State == "closed" {
		t.Fatalf("a new message did not reopen the conversation: %+v", c)
	}
	if err := h.eng.StartForConversation(ctx, ConversationKey{Channel: "wa"}, name, nil); err == nil {
		t.Fatal("a key without a chat was accepted")
	}
	if err := h.eng.StartForConversation(ctx, key, "no-such-workflow", nil); err == nil {
		t.Fatal("an unknown workflow was accepted")
	}
}

func TestConversationKeyFromInput(t *testing.T) {
	k, ok := ConversationKeyFromInput(map[string]any{"message": map[string]any{"channel": "wa", "chat": "1:2@s.whatsapp.net"}})
	if !ok || k.String() != "wa:1:2@s.whatsapp.net" {
		t.Fatalf("%v %v", k, ok)
	}
	back, ok := ParseConversationKey(k.String())
	if !ok || back != k {
		t.Fatalf("round trip %v", back)
	}
	if _, ok := ConversationKeyFromInput(map[string]any{"message": map[string]any{"text": "no chat"}}); ok {
		t.Fatal("a message with no chat is not a conversation")
	}
}
