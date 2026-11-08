package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/wamonitor"
)

// The handler templates are driven here the way production drives them: a
// apl.whatsapp (the real wamonitor over the real apl-whatsapp.js
// adapter, with a fake apl underneath) reads a chat, and its watch POSTs the
// delivery {message, messages, watch, chat} to the workflow's hook. The hook
// here starts the run with that body as its input.

// watchDeliver delivers one new message in chat through watch w to workflow
// wf and returns the run the delivery started.
func watchDeliver(t *testing.T, h *harness, wf string, w wamonitor.Watch, chat, id, text string) uuid.UUID {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	storeFile := filepath.Join(dir, "store.json")
	put := func(rows ...map[string]any) {
		b, _ := json.Marshal(map[string]any{"messages": rows})
		if err := os.WriteFile(storeFile, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	row := func(id, ts, text string) map[string]any {
		return map[string]any{"ChatJID": chat, "MsgID": id, "SenderJID": "1555@s.whatsapp.net",
			"Timestamp": ts, "Text": text, "FromMe": false}
	}
	started := make(chan uuid.UUID, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hook-tok" {
			rw.WriteHeader(401)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			rw.WriteHeader(400)
			return
		}
		started <- h.start(wf, body)
		rw.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)
	w.Hook, w.TokenEnv = srv.URL+"/hooks/p/"+wf, "HOOK_TOK"
	if w.Chat == "" {
		w.Chat = chat
	}
	adapter, _ := filepath.Abs(repoPath(t, "adapters/apl-whatsapp.js"))
	fake, _ := filepath.Abs(filepath.Join("..", "wamonitor", "testdata", "fake-apl.js"))
	m, err := wamonitor.New(wamonitor.Config{
		Receive:   []string{"node", adapter, "receive"},
		Env:       []string{"APL_BIN=" + fake, "APL_WA_LEDGER=" + filepath.Join(dir, "l.jsonl"), "FAKE_WA_STORE=" + storeFile},
		Channel:   "wa",
		Watches:   func(context.Context) ([]wamonitor.Watch, error) { return []wamonitor.Watch{w}, nil },
		StateFile: filepath.Join(dir, "state.json"),
		Getenv: func(k string) string {
			if k == "HOOK_TOK" {
				return "hook-tok"
			}
			return ""
		},
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := row("h0-"+id, "2026-11-08T09:00:00Z", "history")
	put(old)
	if err := m.Read(ctx); err != nil { // anchors the cursor; history is never delivered
		t.Fatal(err)
	}
	put(old, row(id, "2026-11-08T09:01:00Z", text))
	if err := m.Read(ctx); err != nil {
		t.Fatal(err)
	}
	m.Flush(ctx, true)
	select {
	case runID := <-started:
		return runID
	case <-time.After(10 * time.Second):
		t.Fatal("the watch delivered nothing")
	}
	return uuid.Nil
}

func childSender(t *testing.T, h *harness, parent uuid.UUID) map[string]any {
	t.Helper()
	kids, _ := h.store.ChildRuns(context.Background(), parent)
	if len(kids) != 1 || kids[0].Workflow != "whatsapp-sender" {
		t.Fatalf("children of %s: %+v", parent, kids)
	}
	h.waitEnded(kids[0].ID)
	return senderResult(t, h, kids[0].ID)
}

// pingpong: a watch with no policy drafts (never auto-sends); a watch that
// says auto sends into the chat.
func TestPingpongFromAWatchDeliveryDraftsByDefault(t *testing.T) {
	cmd, dir := fakeWA(t, "sent")
	env := map[string]string{"WA_ADAPTER": cmd, "WA_SENDER_LEDGER": filepath.Join(t.TempDir(), "l.jsonl")}
	defs := loadWATemplates(t, env, "pingpong", "whatsapp-sender")
	h := waHarness(t, defs, "pingpong")
	useWAChannel(t, h, cmd, "")

	chat := uniq("g") + "@g.us"
	id := watchDeliver(t, h, "pingpong", wamonitor.Watch{Name: "pp"}, chat, uniq("M"), "ping")
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("pingpong: %s %s", run.Status, run.Error)
	}
	var in map[string]any
	run, _ := h.store.GetRun(context.Background(), id)
	_ = json.Unmarshal(run.Input, &in)
	if w, _ := in["watch"].(map[string]any); w["name"] != "pp" || in["chat"] != chat {
		t.Fatalf("delivery input = %s", run.Input)
	}
	// Memory keys on (channel, chat) from the delivered message.
	if conv := conversationOfRun(run); conv != "wa:"+chat {
		t.Fatalf("conversation = %q, want wa:%s", conv, chat)
	}
	if res := childSender(t, h, id); res["status"] != "drafted" || res["chat"] != chat {
		t.Fatalf("no policy must draft, got %v", res)
	}
	if sent := waSent(t, dir); len(sent) != 0 {
		t.Fatalf("a watch without a policy sent: %v", sent)
	}

	chat2 := uniq("g") + "@g.us"
	id = watchDeliver(t, h, "pingpong", wamonitor.Watch{Name: "pp-auto", ResponsePolicy: "auto"}, chat2, uniq("M"), "ping")
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("pingpong auto: %s %s", run.Status, run.Error)
	}
	if res := childSender(t, h, id); res["status"] != "sent" {
		t.Fatalf("auto watch: %v", res)
	}
	if sent := waSent(t, dir); len(sent) != 1 || sent[0]["target"] != chat2 || sent[0]["text"] != "pong #1" {
		t.Fatalf("sent %v", sent)
	}
}

func frontdeskHarness(t *testing.T, llm *fakeLLM) (*harness, string) {
	t.Helper()
	cmd, dir := fakeWA(t, "sent")
	env := map[string]string{"WA_ADAPTER": cmd, "WA_SENDER_LEDGER": filepath.Join(t.TempDir(), "l.jsonl"),
		"WFX_API": "http://127.0.0.1:1"} // engine.json records the API as unreadable
	defs := loadWATemplates(t, env, "frontdesk", "whatsapp-sender", "pingpong", "retrospective")
	h := newHarness(t, defs["frontdesk"], llm, "")
	for n, d := range defs {
		if n != "frontdesk" {
			h.eng.defs[n] = d
		}
	}
	useWAChannel(t, h, cmd, "")
	return h, dir
}

// frontdesk: a confident answer to a watched chat is drafted, not sent, when
// the watch names no policy.
func TestFrontdeskFromAWatchDeliveryDraftsByDefault(t *testing.T) {
	llm := newFakeLLM(t, submit(map[string]any{"reply": "Open 9-5", "confidence": 0.95, "handoff_to": "none", "reason": "instructions"}), finish())
	h, dir := frontdeskHarness(t, llm)
	chat := uniq("g") + "@g.us"
	id := watchDeliver(t, h, "frontdesk", wamonitor.Watch{Name: "desk"}, chat, uniq("M"), "when are you open?")
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("frontdesk: %s %s", run.Status, run.Error)
	}
	if res := childSender(t, h, id); res["status"] != "drafted" || res["text"] != "Open 9-5" {
		t.Fatalf("frontdesk reply = %v", res)
	}
	if sent := waSent(t, dir); len(sent) != 0 {
		t.Fatalf("frontdesk auto-sent without a policy: %v", sent)
	}
}

// load-context (embedded in frontdesk as ctx.gather): the context comes from
// the delivery — newest message, sender, chat — and the policy from the watch.
func TestLoadContextReadsTheWatchDelivery(t *testing.T) {
	llm := newFakeLLM(t, submit(map[string]any{"reply": "maybe", "confidence": 0.2, "handoff_to": "none", "reason": "unsure"}), finish())
	h, _ := frontdeskHarness(t, llm)
	chat := uniq("g") + "@g.us"
	w := wamonitor.Watch{Name: "desk", ResponsePolicy: "approval", Instructions: "be brief"}
	id := watchDeliver(t, h, "frontdesk", w, chat, uniq("M"), "hello")
	h.wait(id) // a low-confidence answer parks on a person
	got := asStr(stepOut(t, h, id, "ctx.gather")["stdout"])
	if !strings.Contains(got, "context for "+chat+" from 1555@s.whatsapp.net (message), policy approval") {
		t.Fatalf("ctx.gather = %q", got)
	}

	// Without a policy on the watch, load-context's default is draft.
	id = watchDeliver(t, h, "frontdesk", wamonitor.Watch{Name: "desk2"}, uniq("g")+"@g.us", uniq("M"), "hello")
	h.wait(id)
	if got := asStr(stepOut(t, h, id, "ctx.gather")["stdout"]); !strings.HasSuffix(strings.TrimSpace(got), "policy draft") {
		t.Fatalf("default policy: %q", got)
	}
}
