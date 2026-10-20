package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func approvalDef(name string) *workflow.Definition {
	def := &workflow.Definition{Name: name, Steps: []workflow.Step{{
		ID: "ship", Run: "echo shipped", RequiresApproval: true,
	}}}
	normalizeForTest(def)
	return def
}

// announced waits for the pause announcement, which happens just AFTER the
// run parks (a dead channel must not change whether it parked).
func announced(t *testing.T, st *store.Store, runID uuid.UUID) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ev := eventTexts(t, st, runID); strings.Contains(ev, "channel ") {
			return ev
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the pause was never announced on a channel")
	return ""
}

func eventTexts(t *testing.T, st *store.Store, runID uuid.UUID) string {
	t.Helper()
	evs, err := st.ListEvents(context.Background(), runID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		b.Write(ev.Payload)
		b.WriteByte('\n')
	}
	return b.String()
}

// A pause goes to the DEFAULT channel with no notifier registered at all —
// the bug was an early return that told nobody — and the envelope carries a
// pointer and a code, never a way to resolve.
func TestPauseIsSentToTheDefaultChannel(t *testing.T) {
	h := newHarness(t, approvalDef("chpause"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, "ops", false)
	h.eng.cfg.DefaultChannel = "ops"
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("status %s", run.Status)
	}
	announced(t, h.store, run.ID)
	got := sentEnvelopes(t, dir)
	if len(got) != 1 {
		t.Fatalf("adapter saw %d sends, want 1", len(got))
	}
	env := got[0]
	if env.Version != notify.Version || env.Type != notify.TypePause || env.Channel != "ops" {
		t.Fatalf("envelope header: %+v", env)
	}
	if env.Code == "" || !strings.Contains(env.Text, "#"+env.Code+" yes") {
		t.Fatalf("pause must carry a code and say how to use it: code=%q text=%q", env.Code, env.Text)
	}
	if !strings.HasSuffix(env.RunURL, "/runs/"+run.ID.String()) || env.StepID != "ship" || env.PauseKind != "approval" {
		t.Fatalf("pointer fields: %+v", env)
	}
	if !strings.Contains(eventTexts(t, h.store, run.ID), "announced on channel ops") {
		t.Fatal("the delivery must be recorded on the run")
	}
}

// A non-zero adapter exit leaves the run paused and puts the adapter's
// stderr on the run, where somebody will read it.
func TestAdapterFailureIsShownOnTheRun(t *testing.T) {
	h := newHarness(t, approvalDef("chfail"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, "ops", false)
	h.eng.cfg.DefaultChannel = "ops"
	if err := os.WriteFile(filepath.Join(dir, "fail"), []byte("gateway said 503: syncer down"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := h.run(nil)
	if run.Status != "awaiting_approval" {
		t.Fatalf("a dead channel must not change the pause: %s", run.Status)
	}
	if ev := announced(t, h.store, run.ID); !strings.Contains(ev, "gateway said 503: syncer down") {
		t.Fatalf("stderr not on the run:\n%s", ev)
	}
}

// The yes/no rule: a reply quoting the code resolves ONLY from an
// allow-listed sender. Anyone else is recorded and resolves nothing.
func TestChatReplyResolvesOnlyFromAnAllowListedSender(t *testing.T) {
	h := newHarness(t, approvalDef("chreply"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, "opsreply", true, "15550001@s.whatsapp.net")
	h.eng.cfg.DefaultChannel = "opsreply"
	run := h.run(nil)
	announced(t, h.store, run.ID)
	code := sentEnvelopes(t, dir)[0].Code
	ctx := context.Background()

	writeInbox(t, dir,
		notify.Message{ID: "rep-A", From: "15559999@s.whatsapp.net", Name: "mallory", Text: "#" + code + " yes"})
	if n, err := h.eng.ReceiveNow(ctx, "opsreply"); err != nil || n != 1 {
		t.Fatalf("receive n=%d err=%v", n, err)
	}
	if r, _ := h.store.GetRun(ctx, run.ID); r.Status != "awaiting_approval" {
		t.Fatalf("a sender NOT on the allow list resolved the pause: %s", r.Status)
	}
	if !strings.Contains(eventTexts(t, h.store, run.ID), "not on the channel's allow list") {
		t.Fatal("the refused reply must be recorded on the run")
	}

	writeInbox(t, dir,
		notify.Message{ID: "rep-A", From: "15559999@s.whatsapp.net", Text: "#" + code + " yes"},
		notify.Message{ID: "rep-B", From: "15550001@s.whatsapp.net", Name: "ops lead", Text: "#" + strings.ToUpper(code) + " yes ship it"})
	if n, err := h.eng.ReceiveNow(ctx, "opsreply"); err != nil || n != 1 {
		t.Fatalf("second receive must see only B: n=%d err=%v", n, err)
	}
	final := h.wait(run.ID)
	if final.Status != "done" {
		t.Fatalf("allow-listed yes did not resolve: %s", final.Status)
	}
	st := h.steps(run.ID)["ship"]
	if st.ResolvedBy != "15550001@s.whatsapp.net (via channel:opsreply)" || st.Resolution != "approved" {
		t.Fatalf("who answered, via which channel: %q / %q", st.ResolvedBy, st.Resolution)
	}
	msgs, _ := h.eng.ChannelMessages(ctx, "opsreply", "", 10)
	outcomes := map[string]string{}
	for _, m := range msgs {
		outcomes[m.MsgID] = m.Outcome
	}
	if outcomes["rep-A"] != "recorded" || outcomes["rep-B"] != "resolved" {
		t.Fatalf("outcomes %v", outcomes)
	}
}

// "no <reason>" from an allowed sender rejects with that reason; an empty
// allow list resolves nothing at all.
func TestChatRejectAndEmptyAllowList(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, approvalDef("chreject"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, "opsrej", true, "lead")
	h.eng.cfg.DefaultChannel = "opsrej"
	run := h.run(nil)
	announced(t, h.store, run.ID)
	code := sentEnvelopes(t, dir)[0].Code
	writeInbox(t, dir, notify.Message{ID: "rej-1", From: "lead", Text: "#" + code + " no not on a friday"})
	if _, err := h.eng.ReceiveNow(ctx, "opsrej"); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(run.ID); r.Status != "cancelled" {
		t.Fatalf("status %s", r.Status)
	}
	if st := h.steps(run.ID)["ship"]; st.Resolution != "rejected" || st.ResolutionReason != "not on a friday" {
		t.Fatalf("reject not recorded: %q %q", st.Resolution, st.ResolutionReason)
	}

	h2 := newHarness(t, approvalDef("chnobody"), newFakeLLM(t), "")
	dir2 := fakeChannel(t, h2.eng, "opsrej", true) // no allow list
	h2.eng.cfg.DefaultChannel = "opsrej"
	run2 := h2.run(nil)
	announced(t, h2.store, run2.ID)
	code2 := sentEnvelopes(t, dir2)[0].Code
	writeInbox(t, dir2, notify.Message{ID: "rej-1", From: "lead", Text: "#" + code2 + " yes"})
	if _, err := h2.eng.ReceiveNow(ctx, "opsrej"); err != nil {
		t.Fatal(err)
	}
	if r, _ := h2.store.GetRun(ctx, run2.ID); r.Status != "awaiting_approval" {
		t.Fatalf("an empty allow list must resolve nothing: %s", r.Status)
	}
}

// The cursor the adapter returns is persisted and handed back next time, and
// a re-delivered message is not acted on twice.
func TestReceiveCursorIsPersisted(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, approvalDef("chcursor"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, "inbox", true)
	writeInbox(t, dir, notify.Message{ID: "x1", From: "a", Text: "hello"}, notify.Message{ID: "x2", From: "b", Text: "there"})
	if n, err := h.eng.ReceiveNow(ctx, "inbox"); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if c, _ := h.store.Setting(ctx, "channel.cursor.inbox"); c != "x2" {
		t.Fatalf("cursor %q", c)
	}
	if n, _ := h.eng.ReceiveNow(ctx, "inbox"); n != 0 {
		t.Fatalf("nothing new, but %d stored", n)
	}
	reqs := readLines(filepath.Join(dir, "received.jsonl"))
	if len(reqs) != 2 || !strings.Contains(reqs[0], `"after":""`) || !strings.Contains(reqs[1], `"after":"x2"`) {
		t.Fatalf("the adapter must get the persisted cursor back: %v", reqs)
	}
}

// A `message:` trigger starts a run with the message as its input.
func TestMessageTriggerStartsARun(t *testing.T) {
	ctx := context.Background()
	def := &workflow.Definition{Name: "ondeploy",
		On:    workflow.Triggers{Message: []workflow.MessageTrigger{{Channel: "opstrg", Match: `(?i)^deploy\s+\S+`}}},
		Steps: []workflow.Step{{ID: "trg-echo", Run: "echo {{ .Input.message.text }}"}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, "opstrg", true)
	writeInbox(t, dir,
		notify.Message{ID: "trg-1", From: "a", Text: "good morning"},
		notify.Message{ID: "trg-2", From: "a", Text: "Deploy api"})
	if _, err := h.eng.ReceiveNow(ctx, "opstrg"); err != nil {
		t.Fatal(err)
	}
	var runs []*store.Run
	for i := 0; i < 100; i++ {
		runs, _ = h.store.FindRuns(ctx, store.RunFilter{Workflow: "ondeploy"})
		if len(runs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(runs) != 1 {
		t.Fatalf("want exactly one run (only `Deploy api` matches), got %d", len(runs))
	}
	h.runs = append(h.runs, runs[0].ID)
	r := h.wait(runs[0].ID)
	if r.Status != "done" || r.TriggeredBy != "trigger:message" {
		t.Fatalf("run %s triggered by %q", r.Status, r.TriggeredBy)
	}
	if !strings.Contains(string(r.Input), `"Deploy api"`) {
		t.Fatalf("the message must be the run's input: %s", r.Input)
	}
}

// The send step: dedup by key across runs, dry_run sends nothing, a failure
// fails the step with the adapter's stderr.
func TestSendStepDedupDryRunAndFailure(t *testing.T) {
	ctx := context.Background()
	def := &workflow.Definition{Name: "sender", Steps: []workflow.Step{{ID: "tell", Send: &workflow.SendStep{
		Text: "disk full on {{ .Input.host }}", Dedup: &workflow.Dedup{Key: "disk-{{ .Input.host }}", Window: "1h"},
	}}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, "opssend", false)
	h.eng.cfg.DefaultChannel = "opssend"

	if r := h.run(map[string]any{"host": "db1"}); r.Status != "done" {
		t.Fatalf("first run %s: %s", r.Status, r.Error)
	}
	r2 := h.run(map[string]any{"host": "db1"})
	if r2.Status != "done" {
		t.Fatalf("a deduped send is not a failure: %s", r2.Status)
	}
	if out := string(h.steps(r2.ID)["tell"].Output); !strings.Contains(out, `"dedup"`) {
		t.Fatalf("second output %s", out)
	}
	h.run(map[string]any{"host": "db2"})
	got := sentEnvelopes(t, dir)
	if len(got) != 2 || got[0].Text != "disk full on db1" || got[1].Text != "disk full on db2" || got[0].DedupKey != "disk-db1" {
		t.Fatalf("sends %+v", got)
	}
	if got[0].Type != notify.TypeSend || got[0].Workflow != "sender" {
		t.Fatalf("envelope %+v", got[0])
	}

	def.Steps[0].Send.DryRun = true
	r3 := h.run(map[string]any{"host": "db3"})
	if len(sentEnvelopes(t, dir)) != 2 {
		t.Fatal("dry_run must not run the adapter")
	}
	if !strings.Contains(eventTexts(t, h.store, r3.ID), "would send on channel opssend: disk full on db3") {
		t.Fatal("dry_run must say what it would have sent")
	}
	def.Steps[0].Send.DryRun = false

	_ = os.WriteFile(filepath.Join(dir, "fail"), []byte("no route to gateway"), 0o600)
	r4 := h.run(map[string]any{"host": "db4"})
	if r4.Status != "failed" || !strings.Contains(r4.Error+h.steps(r4.ID)["tell"].Error, "no route to gateway") {
		t.Fatalf("failed send: status %s err %q", r4.Status, r4.Error)
	}
	// A failed send releases its key, so the next attempt is not swallowed.
	_ = os.Remove(filepath.Join(dir, "fail"))
	if r := h.run(map[string]any{"host": "db4"}); r.Status != "done" || !strings.Contains(string(h.steps(r.ID)["tell"].Output), `"sent": true`) {
		t.Fatal("a failed send must not count for dedup")
	}
	_ = ctx
}
