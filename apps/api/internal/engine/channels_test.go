package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
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

// namer makes this test invocation's names unique — channels, workflows and
// message ids live in one database for the whole package, and `go test
// -count=2` must not find the first run's cursor, dedup row or runs.
func namer() func(string) string {
	sfx := uuid.NewString()[:8]
	return func(s string) string { return s + "-" + sfx }
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
	n := namer()
	h := newHarness(t, approvalDef("chpause"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("ops"), false)
	h.eng.cfg.DefaultChannel = n("ops")
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
	if env.Version != notify.Version || env.Type != notify.TypePause || env.Channel != n("ops") {
		t.Fatalf("envelope header: %+v", env)
	}
	if env.Code == "" || !strings.Contains(env.Text, "#"+env.Code+" yes") {
		t.Fatalf("pause must carry a code and say how to use it: code=%q text=%q", env.Code, env.Text)
	}
	if !strings.HasSuffix(env.RunURL, "/runs/"+run.ID.String()) || env.StepID != "ship" || env.PauseKind != "approval" {
		t.Fatalf("pointer fields: %+v", env)
	}
	if !strings.Contains(eventTexts(t, h.store, run.ID), "announced on channel "+n("ops")) {
		t.Fatal("the delivery must be recorded on the run")
	}
}

// A non-zero adapter exit leaves the run paused and puts the adapter's
// stderr on the run, where somebody will read it.
func TestAdapterFailureIsShownOnTheRun(t *testing.T) {
	n := namer()
	h := newHarness(t, approvalDef("chfail"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("ops"), false)
	h.eng.cfg.DefaultChannel = n("ops")
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
	n := namer()
	h := newHarness(t, approvalDef("chreply"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("opsreply"), true, "15550001@s.whatsapp.net")
	h.eng.cfg.DefaultChannel = n("opsreply")
	run := h.run(nil)
	announced(t, h.store, run.ID)
	code := sentEnvelopes(t, dir)[0].Code
	ctx := context.Background()

	writeInbox(t, dir,
		notify.Message{ID: n("rep-A"), From: "15559999@s.whatsapp.net", Name: "mallory", Text: "#" + code + " yes"})
	if n, err := h.eng.ReceiveNow(ctx, n("opsreply")); err != nil || n != 1 {
		t.Fatalf("receive n=%d err=%v", n, err)
	}
	if r, _ := h.store.GetRun(ctx, run.ID); r.Status != "awaiting_approval" {
		t.Fatalf("a sender NOT on the allow list resolved the pause: %s", r.Status)
	}
	if !strings.Contains(eventTexts(t, h.store, run.ID), "not on the channel's allow list") {
		t.Fatal("the refused reply must be recorded on the run")
	}

	writeInbox(t, dir,
		notify.Message{ID: n("rep-A"), From: "15559999@s.whatsapp.net", Text: "#" + code + " yes"},
		notify.Message{ID: n("rep-B"), From: "15550001@s.whatsapp.net", Name: "ops lead", Text: "#" + strings.ToUpper(code) + " yes ship it"})
	if n, err := h.eng.ReceiveNow(ctx, n("opsreply")); err != nil || n != 1 {
		t.Fatalf("second receive must see only B: n=%d err=%v", n, err)
	}
	final := h.wait(run.ID)
	if final.Status != "done" {
		t.Fatalf("allow-listed yes did not resolve: %s", final.Status)
	}
	st := h.steps(run.ID)["ship"]
	if st.ResolvedBy != "15550001@s.whatsapp.net (via channel:"+n("opsreply")+")" || st.Resolution != "approved" {
		t.Fatalf("who answered, via which channel: %q / %q", st.ResolvedBy, st.Resolution)
	}
	msgs, _ := h.eng.ChannelMessages(ctx, model.ChannelMessageFilter{Channel: n("opsreply"), All: true, Limit: 10})
	outcomes := map[string]string{}
	for _, m := range msgs {
		outcomes[m.MsgID] = m.Outcome
	}
	if outcomes[n("rep-A")] != "recorded" || outcomes[n("rep-B")] != "resolved" {
		t.Fatalf("outcomes %v", outcomes)
	}
}

// "no <reason>" from an allowed sender rejects with that reason; an empty
// allow list resolves nothing at all.
func TestChatRejectAndEmptyAllowList(t *testing.T) {
	n := namer()
	ctx := context.Background()
	h := newHarness(t, approvalDef("chreject"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("opsrej"), true, "lead")
	h.eng.cfg.DefaultChannel = n("opsrej")
	run := h.run(nil)
	announced(t, h.store, run.ID)
	code := sentEnvelopes(t, dir)[0].Code
	writeInbox(t, dir, notify.Message{ID: n("rej-1"), From: "lead", Text: "#" + code + " no not on a friday"})
	if _, err := h.eng.ReceiveNow(ctx, n("opsrej")); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(run.ID); r.Status != "cancelled" {
		t.Fatalf("status %s", r.Status)
	}
	if st := h.steps(run.ID)["ship"]; st.Resolution != "rejected" || st.ResolutionReason != "not on a friday" {
		t.Fatalf("reject not recorded: %q %q", st.Resolution, st.ResolutionReason)
	}

	h2 := newHarness(t, approvalDef("chnobody"), newFakeLLM(t), "")
	dir2 := fakeChannel(t, h2.eng, n("opsrej"), true) // no allow list
	h2.eng.cfg.DefaultChannel = n("opsrej")
	run2 := h2.run(nil)
	announced(t, h2.store, run2.ID)
	code2 := sentEnvelopes(t, dir2)[0].Code
	writeInbox(t, dir2, notify.Message{ID: n("rej-1"), From: "lead", Text: "#" + code2 + " yes"})
	if _, err := h2.eng.ReceiveNow(ctx, n("opsrej")); err != nil {
		t.Fatal(err)
	}
	if r, _ := h2.store.GetRun(ctx, run2.ID); r.Status != "awaiting_approval" {
		t.Fatalf("an empty allow list must resolve nothing: %s", r.Status)
	}
}

// The cursor the adapter returns is persisted and handed back next time, and
// a re-delivered message is not acted on twice.
func TestReceiveCursorIsPersisted(t *testing.T) {
	n := namer()
	ctx := context.Background()
	h := newHarness(t, approvalDef("chcursor"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("inbox"), true)
	writeInbox(t, dir, notify.Message{ID: n("x1"), From: "a", Text: "hello"}, notify.Message{ID: n("x2"), From: "b", Text: "there"})
	if n, err := h.eng.ReceiveNow(ctx, n("inbox")); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if c, _ := h.store.Setting(ctx, "channel.cursor."+n("inbox")); c != n("x2") {
		t.Fatalf("cursor %q", c)
	}
	if n, _ := h.eng.ReceiveNow(ctx, n("inbox")); n != 0 {
		t.Fatalf("nothing new, but %d stored", n)
	}
	reqs := readLines(filepath.Join(dir, "received.jsonl"))
	if len(reqs) != 2 || !strings.Contains(reqs[0], `"after":""`) || !strings.Contains(reqs[1], `"after":"`+n("x2")+`"`) {
		t.Fatalf("the adapter must get the persisted cursor back: %v", reqs)
	}
}

// A `message:` trigger starts a run with the message as its input.
func TestMessageTriggerStartsARun(t *testing.T) {
	n := namer()
	ctx := context.Background()
	def := &workflow.Definition{Name: n("ondeploy"),
		On:    workflow.Triggers{Message: []workflow.MessageTrigger{{Channel: n("opstrg"), Match: `(?i)^deploy\s+\S+`}}},
		Steps: []workflow.Step{{ID: "trg-echo", Run: "echo {{ .Input.message.text }}"}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("opstrg"), true, "a")
	writeInbox(t, dir,
		notify.Message{ID: n("trg-1"), From: "a", Text: "good morning"},
		notify.Message{ID: n("trg-2"), From: "a", Text: "Deploy api"})
	if _, err := h.eng.ReceiveNow(ctx, n("opstrg")); err != nil {
		t.Fatal(err)
	}
	var runs []*store.Run
	for i := 0; i < 100; i++ {
		runs, _ = h.store.FindRuns(ctx, store.RunFilter{Workflow: n("ondeploy")})
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
	if r.Status != "done" || r.TriggeredBy != "a" {
		t.Fatalf("run %s triggered by %q", r.Status, r.TriggeredBy)
	}
	if !strings.Contains(string(r.Input), `"Deploy api"`) {
		t.Fatalf("the message must be the run's input: %s", r.Input)
	}
}

// The send step: dedup by key across runs, dry_run sends nothing, a failure
// fails the step with the adapter's stderr.
func TestSendStepDedupDryRunAndFailure(t *testing.T) {
	n := namer()
	ctx := context.Background()
	def := &workflow.Definition{Name: n("sender"), Steps: []workflow.Step{{ID: "tell", Send: &workflow.SendStep{
		Text: "disk full on {{ .Input.host }}", Dedup: &workflow.Dedup{Key: "disk-{{ .Input.host }}", Window: "1h"},
	}}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("opssend"), false)
	h.eng.cfg.DefaultChannel = n("opssend")

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
	if got[0].Type != notify.TypeSend || got[0].Workflow != n("sender") {
		t.Fatalf("envelope %+v", got[0])
	}

	def.Steps[0].Send.DryRun = true
	r3 := h.run(map[string]any{"host": "db3"})
	if len(sentEnvelopes(t, dir)) != 2 {
		t.Fatal("dry_run must not run the adapter")
	}
	if !strings.Contains(eventTexts(t, h.store, r3.ID), "would send on channel "+n("opssend")+": disk full on db3") {
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

// Anyone in a group must NOT be able to start a run: a message trigger fires
// only for a sender on its allow list, else the channel's; both empty ⇒ never.
func TestMessageTriggerRequiresAnAllowListedSender(t *testing.T) {
	n := namer()
	ctx := context.Background()
	def := &workflow.Definition{Name: n("ondeploy2"),
		On:    workflow.Triggers{Message: []workflow.MessageTrigger{{Channel: n("trgallow"), Contains: "deploy"}}},
		Steps: []workflow.Step{{ID: "echo", Run: "echo hi"}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("trgallow"), true) // channel allow list EMPTY
	writeInbox(t, dir, notify.Message{ID: n("ta-1"), From: "lead", Text: "deploy api"})
	if _, err := h.eng.ReceiveNow(ctx, n("trgallow")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if runs, _ := h.store.FindRuns(ctx, store.RunFilter{Workflow: n("ondeploy2")}); len(runs) != 0 {
		t.Fatalf("an empty allow list must start nothing, got %d runs", len(runs))
	}
	msgs, _ := h.eng.ChannelMessages(ctx, model.ChannelMessageFilter{Channel: n("trgallow"), All: true, Limit: 5})
	if len(msgs) != 1 || msgs[0].Outcome != "refused" {
		t.Fatalf("the refusal must be recorded: %+v", msgs)
	}

	// The trigger's own list overrides the (empty) channel list.
	def.On.Message[0].Allow = []string{"lead"}
	writeInbox(t, dir, notify.Message{ID: n("ta-1"), From: "lead", Text: "deploy api"},
		notify.Message{ID: n("ta-2"), From: "stranger", Text: "deploy api"},
		notify.Message{ID: n("ta-3"), From: "lead", Text: "deploy web"})
	if _, err := h.eng.ReceiveNow(ctx, n("trgallow")); err != nil {
		t.Fatal(err)
	}
	var runs []*store.Run
	for i := 0; i < 100; i++ {
		if runs, _ = h.store.FindRuns(ctx, store.RunFilter{Workflow: n("ondeploy2")}); len(runs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	runs, _ = h.store.FindRuns(ctx, store.RunFilter{Workflow: n("ondeploy2")})
	if len(runs) != 1 || runs[0].TriggeredBy != "lead" {
		t.Fatalf("only lead's message may start a run, attributed to lead: %d runs", len(runs))
	}
	h.runs = append(h.runs, runs[0].ID)
	h.wait(runs[0].ID)
}

// A code is retired when its pause resolves by ANY path, so replaying it after
// the step pauses again cannot answer the new pause; and a code only matches on
// the channel it was sent on.
func TestPauseCodeReplayAndOtherChannel(t *testing.T) {
	n := namer()
	ctx := context.Background()
	def := &workflow.Definition{Name: n("chreplay"), Steps: []workflow.Step{{
		ID: "ship", Run: "exit 1", RequiresApproval: true,
	}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("opsreplay"), true, "lead")
	other := fakeChannel(t, h.eng, n("otherch"), true, "lead")
	h.eng.cfg.DefaultChannel = n("opsreplay")
	run := h.run(nil)
	announced(t, h.store, run.ID)
	code1 := sentEnvelopes(t, dir)[0].Code
	if len(code1) != 6 {
		t.Fatalf("codes are 6 characters: %q", code1)
	}

	// The right code on the WRONG channel matches nothing.
	writeInbox(t, other, notify.Message{ID: n("oc-1"), From: "lead", Text: "#" + code1 + " yes"})
	if _, err := h.eng.ReceiveNow(ctx, n("otherch")); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.store.GetRun(ctx, run.ID); r.Status != "awaiting_approval" {
		t.Fatalf("a code from another channel resolved the pause: %s", r.Status)
	}

	// Approved in the UI; the step fails; retry pauses it again with a new code.
	if err := h.eng.Approve(ctx, run.ID, "ship", Actor{ID: "ada", Via: "ui"}); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(run.ID); r.Status != "failed" {
		t.Fatalf("status %s", r.Status)
	}
	// Start ignores a run still registered as executing (see gates_test.go).
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		h.eng.mu.Lock()
		_, busy := h.eng.running[run.ID]
		h.eng.mu.Unlock()
		if !busy {
			break
		}
	}
	if err := h.eng.Retry(ctx, run.ID, "ship"); err != nil {
		t.Fatal(err)
	}
	h.wait(run.ID)
	for i := 0; i < 200 && len(sentEnvelopes(t, dir)) < 2; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	envs := sentEnvelopes(t, dir)
	if len(envs) != 2 || envs[1].Code == code1 {
		t.Fatalf("the second pause needs its own code: %+v", envs)
	}
	writeInbox(t, dir, notify.Message{ID: n("rp-1"), From: "lead", Text: "#" + code1 + " yes"})
	if _, err := h.eng.ReceiveNow(ctx, n("opsreplay")); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.store.GetRun(ctx, run.ID); r.Status != "awaiting_approval" {
		t.Fatalf("a replayed old code answered the new pause: %s", r.Status)
	}
	if !strings.Contains(eventTexts(t, h.store, run.ID), "no longer live") {
		t.Fatal("the replay must be recorded on the run")
	}

	// A dead code FOLLOWED by the live one: every code is tried, and the
	// live one answers.
	code2 := envs[1].Code
	writeInbox(t, dir, notify.Message{ID: n("rp-1"), From: "lead", Text: "#" + code1 + " yes"},
		notify.Message{ID: n("rp-2"), From: "lead", Text: "#" + code1 + " #" + code2 + " yes"})
	if _, err := h.eng.ReceiveNow(ctx, n("opsreplay")); err != nil {
		t.Fatal(err)
	}
	h.wait(run.ID)
	if st := h.steps(run.ID)["ship"]; st.Resolution != "approved" || !strings.HasPrefix(st.ResolvedBy, "lead ") {
		t.Fatalf("the live code after a dead one must answer: %q by %q", st.Resolution, st.ResolvedBy)
	}
}

// A message re-handled after a crash (stored, run started, outcome never
// recorded) must not start the workflow a second time.
func TestMessageTriggerStartsOnceEvenWhenReHandled(t *testing.T) {
	n := namer()
	ctx := context.Background()
	def := &workflow.Definition{Name: n("once"),
		On:    workflow.Triggers{Message: []workflow.MessageTrigger{{Channel: n("opsonce"), Contains: "deploy"}}},
		Steps: []workflow.Step{{ID: "echo", Run: "echo hi"}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("opsonce"), true, "lead")
	writeInbox(t, dir, notify.Message{ID: n("once-1"), From: "lead", Text: "deploy api"})
	if _, err := h.eng.ReceiveNow(ctx, n("opsonce")); err != nil {
		t.Fatal(err)
	}
	// The crash: the message is pending again, as if its outcome was never
	// written after the run started.
	if _, err := h.store.DB().ExecContext(ctx, `UPDATE channel_messages SET handled=false WHERE channel=$1 AND msg_id=$2`,
		n("opsonce"), n("once-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.ReceiveNow(ctx, n("opsonce")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	runs, _ := h.store.FindRuns(ctx, store.RunFilter{Workflow: n("once")})
	for _, r := range runs {
		h.runs = append(h.runs, r.ID)
	}
	if len(runs) != 1 {
		t.Fatalf("a re-handled message started the workflow %d times, want 1", len(runs))
	}
}

// The pause code is issued synchronously, so a pause answered at once leaves
// no live code behind even while the send is still in flight; and a shutdown
// drains in-flight announcements within its bound.
func TestPauseCodeIsIssuedBeforeTheSendAndAnnouncementsDrain(t *testing.T) {
	n := namer()
	ctx := context.Background()
	h := newHarness(t, approvalDef(n("chdrain")), newFakeLLM(t), "")
	dir := fakeChannelWith(t, h.eng, catalog.Channel{Name: n("opsdrain"), TimeoutSec: 2})
	h.eng.cfg.DefaultChannel = n("opsdrain")
	_ = os.WriteFile(filepath.Join(dir, "hang"), nil, 0o600) // the send hangs past its timeout
	run := h.run(nil)
	if err := h.eng.Approve(ctx, run.ID, "ship", Actor{ID: "ada", Via: "ui"}); err != nil {
		t.Fatal(err)
	}
	h.wait(run.ID)
	if h.eng.DrainAnnouncements(50 * time.Millisecond) {
		t.Fatal("the send is still hanging; the drain must report it did not finish")
	}
	var live int
	if err := h.store.DB().QueryRowContext(ctx, `SELECT count(*) FROM pause_codes WHERE run_id=$1 AND retired_at IS NULL`,
		run.ID.String()).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d live code(s) left for a pause that was already answered", live)
	}
	if !h.eng.DrainAnnouncements(10 * time.Second) {
		t.Fatal("announcements must drain once the adapter is killed at its timeout")
	}
}

// A message stored but not handled (a crash between the two) is handled on
// the next poll — a "#code yes" is never lost.
func TestPendingMessageIsHandledOnTheNextPoll(t *testing.T) {
	n := namer()
	ctx := context.Background()
	h := newHarness(t, approvalDef("chpending"), newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("opspend"), true, "lead")
	h.eng.cfg.DefaultChannel = n("opspend")
	run := h.run(nil)
	announced(t, h.store, run.ID)
	code := sentEnvelopes(t, dir)[0].Code
	// As if the previous poll stored it and then died.
	if _, err := h.store.InsertChannelMessage(ctx, &store.ChannelMessage{Channel: n("opspend"), MsgID: n("crash-1"), Sender: "lead", Text: "#" + code + " yes"}); err != nil {
		t.Fatal(err)
	}
	writeInbox(t, dir)
	if _, err := h.eng.ReceiveNow(ctx, n("opspend")); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(run.ID); r.Status != "done" {
		t.Fatalf("the pending reply was lost: %s", r.Status)
	}
}

// A send cut off by the timeout keeps its dedup key — it may have been
// delivered — and a send step cannot attach a file outside its workspace or an
// internal URL.
func TestSendTimeoutKeepsDedupAndAttachmentsAreConfined(t *testing.T) {
	n := namer()
	def := &workflow.Definition{Name: n("sendhard"), Steps: []workflow.Step{{ID: "tell", Send: &workflow.SendStep{
		Text: "x", Dedup: &workflow.Dedup{Key: "k", Window: "1h"},
	}}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannelWith(t, h.eng, catalog.Channel{Name: n("opshang"), TimeoutSec: 1})
	h.eng.cfg.DefaultChannel = n("opshang")
	_ = os.WriteFile(filepath.Join(dir, "hang"), nil, 0o600)
	if r := h.run(nil); r.Status != "failed" {
		t.Fatalf("a timed-out send fails the step: %s", r.Status)
	}
	_ = os.Remove(filepath.Join(dir, "hang"))
	r := h.run(nil)
	if out := string(h.steps(r.ID)["tell"].Output); !strings.Contains(out, "dedup") {
		t.Fatalf("a timed-out send must keep its key (it may have landed); got %s / %s", r.Status, out)
	}
	if len(sentEnvelopes(t, dir)) != 0 {
		t.Fatal("nothing should have been sent the second time")
	}

	def.Steps[0].Send.Dedup = nil
	for _, a := range []string{"/etc/passwd", "../../outside.txt", "https://127.0.0.1/x", "http://example.com/x"} {
		def.Steps[0].Send.Attachment = a
		r := h.run(nil)
		if r.Status != "failed" {
			t.Fatalf("attachment %q must be refused, run %s", a, r.Status)
		}
	}
}

// The sender who STARTED a run by message is its triggeredBy, so
// prevent_self_approval refuses that same sender approving it from chat.
func TestMessageStarterCannotSelfApprove(t *testing.T) {
	n := namer()
	ctx := context.Background()
	def := &workflow.Definition{Name: n("selfappr"),
		On:    workflow.Triggers{Message: []workflow.MessageTrigger{{Channel: n("opsself"), Contains: "deploy"}}},
		Steps: []workflow.Step{{ID: "ship", Run: "echo ok", RequiresApproval: true, PreventSelfApproval: true}}}
	normalizeForTest(def)
	h := newHarness(t, def, newFakeLLM(t), "")
	dir := fakeChannel(t, h.eng, n("opsself"), true, "lead", "boss")
	h.eng.cfg.DefaultChannel = n("opsself")
	writeInbox(t, dir, notify.Message{ID: n("sa-1"), From: "lead", Text: "deploy api"})
	if _, err := h.eng.ReceiveNow(ctx, n("opsself")); err != nil {
		t.Fatal(err)
	}
	var runs []*store.Run
	for i := 0; i < 100 && len(runs) == 0; i++ {
		runs, _ = h.store.FindRuns(ctx, store.RunFilter{Workflow: n("selfappr")})
		time.Sleep(20 * time.Millisecond)
	}
	if len(runs) != 1 {
		t.Fatal("no run started")
	}
	h.runs = append(h.runs, runs[0].ID)
	h.wait(runs[0].ID)
	announced(t, h.store, runs[0].ID)
	code := sentEnvelopes(t, dir)[0].Code
	writeInbox(t, dir, notify.Message{ID: n("sa-1"), From: "lead", Text: "deploy api"},
		notify.Message{ID: n("sa-2"), From: "lead", Text: "#" + code + " yes"})
	if _, err := h.eng.ReceiveNow(ctx, n("opsself")); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.store.GetRun(ctx, runs[0].ID); r.Status != "awaiting_approval" {
		t.Fatalf("the starter approved their own run: %s", r.Status)
	}
	writeInbox(t, dir, notify.Message{ID: n("sa-2"), From: "lead", Text: "#" + code + " yes"},
		notify.Message{ID: n("sa-3"), From: "boss", Text: "#" + code + " yes"})
	if _, err := h.eng.ReceiveNow(ctx, n("opsself")); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(runs[0].ID); r.Status != "done" {
		t.Fatalf("a different allowed sender should approve: %s", r.Status)
	}
}
