package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func subDef(name string) *workflow.Definition {
	def := &workflow.Definition{Name: name, On: workflow.Triggers{Dispatch: true},
		Steps: []workflow.Step{{ID: "echo", Run: "echo {{ .Input.message.text }}"}}}
	normalizeForTest(def)
	return def
}

func addSub(e *Engine, s catalog.Subscription) {
	started := time.Now().Add(-time.Hour)
	if s.StartedAt == nil {
		s.StartedAt = &started
	}
	if s.Status == "" {
		s.Status = catalog.SubActive
	}
	e.catalog.Subscriptions[s.Name] = s
}

func runsOf(t *testing.T, h *harness, wf string, want int) []*store.Run {
	t.Helper()
	var runs []*store.Run
	for i := 0; i < 50; i++ {
		runs, _ = h.store.FindRuns(context.Background(), store.RunFilter{Workflow: wf})
		if len(runs) >= want && want > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, r := range runs {
		h.runs = append(h.runs, r.ID)
	}
	return runs
}

func deliveries(t *testing.T, h *harness, sub string) map[string]string {
	t.Helper()
	ds, err := h.eng.SubscriptionDeliveries(context.Background(), sub, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range ds {
		out[d.MsgID] = d.Outcome
	}
	return out
}

// A DM from an allow-listed sender starts the target with input.message and
// input.subscription; strangers, backlog and a repeat start nothing.
func TestSubscriptionRoutesADirectMessage(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, subDef("subdm"), newFakeLLM(t), "")
	h.eng.SetConversationDebounce(10 * time.Millisecond)
	dir := fakeChannel(t, h.eng, "wa-dm", false)
	addSub(h.eng, catalog.Subscription{Name: "dm-sub", Channel: "wa-dm", Chat: "alice@s", Workflow: "subdm",
		Allow: []string{"alice"}, Instructions: "be brief", ResponsePolicy: "auto"})
	old := time.Now().Add(-2 * time.Hour)
	now := time.Now()
	writeInbox(t, dir,
		notify.Message{ID: "d-1", From: "alice", Text: "ping", Chat: "alice@s", ChatType: "dm", At: &now},
		notify.Message{ID: "d-2", From: "mallory", Text: "hi", Chat: "alice@s", ChatType: "dm", At: &now},
		notify.Message{ID: "d-3", From: "alice", Text: "old", Chat: "alice@s", ChatType: "dm", At: &old},
		notify.Message{ID: "d-4", From: "alice", Text: "elsewhere", Chat: "bob@s", ChatType: "dm", At: &now})
	if _, err := h.eng.ReceiveNow(ctx, "wa-dm"); err != nil {
		t.Fatal(err)
	}
	runs := runsOf(t, h, "subdm", 1)
	if len(runs) != 1 {
		t.Fatalf("want one run, got %d", len(runs))
	}
	r := h.wait(runs[0].ID)
	in := strings.ReplaceAll(string(r.Input), " ", "")
	for _, want := range []string{`"ping"`, `"dm-sub"`, `"bebrief"`, `"auto"`, `"chatType":"dm"`, `"chat":"alice@s"`} {
		if !strings.Contains(in, want) {
			t.Fatalf("input lacks %s: %s", want, in)
		}
	}
	got := deliveries(t, h, "dm-sub")
	if got["d-1"] != DeliveryStarted || got["d-2"] != DeliveryRefused || got["d-3"] != DeliveryBacklog {
		t.Fatalf("deliveries %v", got)
	}
	if _, ok := got["d-4"]; ok {
		t.Fatalf("another chat must not match: %v", got)
	}
	// The same message offered again starts nothing.
	m := notify.Message{ID: "d-1", From: "alice", Text: "ping", Chat: "alice@s", ChatType: "dm", At: &now}
	c, _ := h.eng.channelEntry("wa-dm")
	if runs, _ := h.eng.routeSubscriptions(ctx, c, m); len(runs) != 0 {
		t.Fatalf("a repeat started %d runs", len(runs))
	}
}

// In a group, mention activation needs a mention of us or a reply to us;
// `always` takes everything from allowed senders.
func TestSubscriptionGroupActivation(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, subDef("subgrp"), newFakeLLM(t), "")
	h.eng.SetConversationDebounce(10 * time.Millisecond)
	fakeChannelWith(t, h.eng, catalog.Channel{Name: "wa-grp", Self: []string{"me@s"}})
	addSub(h.eng, catalog.Subscription{Name: "grp", Channel: "wa-grp", Chat: "g@g", Workflow: "subgrp",
		Allow: []string{"*"}, Mode: catalog.ModeFanout})
	c, _ := h.eng.channelEntry("wa-grp")
	g := func(id string, mut func(*notify.Message)) notify.Message {
		m := notify.Message{ID: id, From: "bob", Text: "x", Chat: "g@g", ChatType: "group"}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	h.eng.routeSubscriptions(ctx, c, g("g-1", nil))
	h.eng.routeSubscriptions(ctx, c, g("g-2", func(m *notify.Message) { m.Mentions = []string{"me@s"} }))
	h.eng.routeSubscriptions(ctx, c, g("g-3", func(m *notify.Message) { m.ReplyTo = &notify.ReplyRef{ID: "z", Sender: "me@s"} }))
	h.eng.routeSubscriptions(ctx, c, g("g-4", func(m *notify.Message) { m.Kind = "reaction" }))
	got := deliveries(t, h, "grp")
	if got["g-1"] != DeliveryInactive || got["g-2"] != DeliveryStarted || got["g-3"] != DeliveryStarted {
		t.Fatalf("deliveries %v", got)
	}
	if _, ok := got["g-4"]; ok {
		t.Fatal("a reaction must not match a message-only subscription")
	}
	runsOf(t, h, "subgrp", 1) // g-2 and g-3 share one debounced conversation run
}

// exclusive takes the message from fanout subscriptions; observe records it
// and starts nothing; a paused subscription sees nothing.
func TestSubscriptionModes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, subDef("submode"), newFakeLLM(t), "")
	h.eng.SetConversationDebounce(10 * time.Millisecond)
	fakeChannel(t, h.eng, "wa-mode", false)
	base := catalog.Subscription{Channel: "wa-mode", Chat: "c@s", Workflow: "submode", Allow: []string{"a"}}
	for name, mode := range map[string]string{"x-excl": catalog.ModeExclusive, "f-fan": catalog.ModeFanout, "o-obs": catalog.ModeObserve} {
		s := base
		s.Name, s.Mode = name, mode
		addSub(h.eng, s)
	}
	p := base
	p.Name, p.Status = "paused", catalog.SubPaused
	addSub(h.eng, p)
	c, _ := h.eng.channelEntry("wa-mode")
	runs, matched := h.eng.routeSubscriptions(ctx, c, notify.Message{ID: "m-1", From: "a", Text: "hi", Chat: "c@s"})
	if len(runs) != 0 || matched != 3 { // queued on the conversation lane, not started inline
		t.Fatalf("runs=%d matched=%d", len(runs), matched)
	}
	for sub, want := range map[string]string{"x-excl": DeliveryStarted, "f-fan": DeliverySkipped, "o-obs": DeliveryObserved} {
		if got := deliveries(t, h, sub)["m-1"]; got != want {
			t.Fatalf("%s: %q, want %q", sub, got, want)
		}
	}
	if len(deliveries(t, h, "paused")) != 0 {
		t.Fatal("a paused subscription must see nothing")
	}
	runsOf(t, h, "submode", 1)
}

// A subscription whose target is missing does not route, and says why.
func TestSubscriptionCheck(t *testing.T) {
	h := newHarness(t, subDef("subchk"), newFakeLLM(t), "")
	fakeChannel(t, h.eng, "wa-chk", false)
	addSub(h.eng, catalog.Subscription{Name: "bad", Channel: "wa-chk", Chat: "c", Workflow: "nope", Allow: []string{"*"}})
	info, ok := h.eng.GetSubscription("bad")
	if !ok || !strings.Contains(info.Problem, "not loaded") {
		t.Fatalf("problem %q", info.Problem)
	}
	// An active subscription makes a non-inbound channel readable, at its interval.
	addSub(h.eng, catalog.Subscription{Name: "fast", Channel: "wa-chk", Chat: "c", Workflow: "subchk", Interval: "30s"})
	c, _ := h.eng.channelEntry("wa-chk")
	if every, reads := h.eng.receiveEvery(c); !reads || every != 30*time.Second {
		t.Fatalf("every=%s reads=%v", every, reads)
	}
}

// A channel with no `self` list asks the adapter who we are; when the adapter
// cannot say, a group subscription carries a warning and starts nothing.
func TestSubscriptionSelfFromAdapter(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, subDef("subself"), newFakeLLM(t), "")
	h.eng.SetConversationDebounce(10 * time.Millisecond)
	dirKnown := fakeChannel(t, h.eng, "wa-known", false)
	if err := os.WriteFile(filepath.Join(dirKnown, "self"), []byte("me@s"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeChannel(t, h.eng, "wa-blind", false)
	addSub(h.eng, catalog.Subscription{Name: "known", Channel: "wa-known", Chat: "g@g.us", Workflow: "subself", Allow: []string{"*"}})
	addSub(h.eng, catalog.Subscription{Name: "blind", Channel: "wa-blind", Chat: "g@g.us", Workflow: "subself", Allow: []string{"*"}})
	if info, _ := h.eng.GetSubscription("known"); info.Warning != "" {
		t.Fatalf("adapter reported self, no warning expected: %q", info.Warning)
	}
	if info, _ := h.eng.GetSubscription("blind"); !strings.Contains(info.Warning, "self") {
		t.Fatalf("want a self warning, got %q", info.Warning)
	}
	m := notify.Message{ID: "s-1", From: "bob", Text: "x", Chat: "g@g.us", ChatType: "group", Mentions: []string{"me@s"}}
	ck, _ := h.eng.channelEntry("wa-known")
	cb, _ := h.eng.channelEntry("wa-blind")
	h.eng.routeSubscriptions(ctx, ck, m)
	h.eng.routeSubscriptions(ctx, cb, m)
	if got := deliveries(t, h, "known")["s-1"]; got != DeliveryStarted {
		t.Fatalf("known: %q", got)
	}
	if got := deliveries(t, h, "blind")["s-1"]; got != DeliveryInactive {
		t.Fatalf("blind: %q", got)
	}
	runsOf(t, h, "subself", 1)
}

// A workflow.completed event from a run in a conversation goes through the
// default event sink to the subscriptions of that chat that list the event,
// once per child run; a chat with no such subscription falls back (false).
func TestSubscriptionRoutesWorkflowCompleted(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, subDef("subdone"), newFakeLLM(t), "")
	fakeChannel(t, h.eng, "wa-done", false)
	addSub(h.eng, catalog.Subscription{Name: "done-sub", Channel: "wa-done", Chat: "c@s", Workflow: "subdone",
		Events: []string{"workflow.completed"}})
	ev := map[string]any{"type": "workflow.completed", "runId": "child-1", "channel": "wa-done", "chat": "c@s", "status": "done"}
	if !h.eng.eventSink(ctx, ev) {
		t.Fatal("the default sink must route workflow.completed to the subscription")
	}
	if !h.eng.eventSink(ctx, ev) {
		t.Fatal("a repeat is still handled (already queued)")
	}
	if got := deliveries(t, h, "done-sub")["workflow.completed:child-1"]; got != DeliveryStarted {
		t.Fatalf("delivery %q", got)
	}
	runsOf(t, h, "subdone", 1)
	time.Sleep(100 * time.Millisecond)
	runs, _ := h.store.FindRuns(ctx, store.RunFilter{Workflow: "subdone"})
	if len(runs) != 1 {
		t.Fatalf("want exactly one run, got %d", len(runs))
	}
	if in := string(runs[0].Input); !strings.Contains(in, "done-sub") || !strings.Contains(in, "child-1") {
		t.Fatalf("input %s", in)
	}
	other := map[string]any{"type": "workflow.completed", "runId": "child-2", "channel": "wa-done", "chat": "zz@s"}
	if h.eng.eventSink(ctx, other) {
		t.Fatal("no subscription for that chat: the sink must decline")
	}
}
