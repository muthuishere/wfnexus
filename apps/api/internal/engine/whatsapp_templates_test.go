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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// THE DEFAULT WHATSAPP WORKFLOWS (ADR 0026), run for real against the engine
// and store, with a FAKE channel adapter — nothing reaches WhatsApp. Every step
// exercised here is `run:` or `start:`, so no model is involved; frontdesk's
// agent step is covered by the dry run.

// fakeWA writes a channel adapter that records every send envelope in
// sent.jsonl and answers per $FAKE_WA_MODE (sent | failed | uncertain).
func fakeWA(t *testing.T, mode string) (cmd, dir string) {
	t.Helper()
	dir = t.TempDir()
	script := filepath.Join(dir, "fake-wa.py")
	body := `import json, os, sys
verb = sys.argv[-1]
env = json.load(sys.stdin)
d = os.path.dirname(os.path.abspath(__file__))
with open(os.path.join(d, "sent.jsonl"), "a") as f:
    f.write(json.dumps(env) + "\n")
mode = "` + mode + `"
n = sum(1 for _ in open(os.path.join(d, "sent.jsonl")))
if mode == "sent":
    print(json.dumps({"id": "wamid-%d" % n, "status": "sent"}))
elif mode == "uncertain":
    print(json.dumps({"status": "uncertain", "error": "lost answer"})); sys.exit(1)
else:
    print(json.dumps({"status": "failed", "error": "not on WhatsApp"})); sys.exit(1)
`
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return "python3 " + script, dir
}

func waSent(t *testing.T, dir string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range readLines(filepath.Join(dir, "sent.jsonl")) {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// loadWATemplates loads the shipped templates through the real loader and
// returns them as runnable copies (what "copy the template" produces).
func loadWATemplates(t *testing.T, env map[string]string, names ...string) map[string]*workflow.Definition {
	t.Helper()
	reg := skills.Load(repoPath(t, "skills"))
	cat, err := catalog.Load(repoPath(t, "registries.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	defs, skips, err := workflow.LoadSources([]workflow.Source{{Name: "templates", Dir: repoPath(t, "templates")}}, catalog.NewValidator(reg, cat))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skips {
		t.Errorf("%s did not load: %s", s.Workflow, s.Reason)
	}
	out := map[string]*workflow.Definition{}
	for _, n := range names {
		var d *workflow.Definition
		for _, x := range defs {
			if x.Name == n {
				d = x
			}
		}
		if d == nil {
			t.Fatalf("template %s not loaded", n)
		}
		cp := *d
		cp.Template = workflow.TemplateInfo{}
		// Workflow env is folded into every step at load; override it there.
		cp.Steps = append([]workflow.Step(nil), d.Steps...)
		for i := range cp.Steps {
			cp.Steps[i].Env = workflow.MergeEnv(cp.Steps[i].Env, env)
		}
		out[n] = &cp
	}
	return out
}

func waHarness(t *testing.T, defs map[string]*workflow.Definition, first string) *harness {
	t.Helper()
	list := []*workflow.Definition{defs[first]}
	for n, d := range defs {
		if n != first {
			list = append(list, d)
		}
	}
	return startHarness(t, list...)
}

func senderResult(t *testing.T, h *harness, id uuid.UUID) map[string]any {
	t.Helper()
	out := stepOut(t, h, id, "deliver")
	var res map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(asStr(out["stdout"]))), &res); err != nil {
		t.Fatalf("deliver did not print the typed result: %v %v", out, err)
	}
	return res
}

func asStr(v any) string { s, _ := v.(string); return s }

func TestWhatsAppSenderPolicies(t *testing.T) {
	cmd, dir := fakeWA(t, "sent")
	ledger := filepath.Join(t.TempDir(), "ledger.jsonl")
	defs := loadWATemplates(t, map[string]string{"WA_ADAPTER": cmd, "WA_SENDER_LEDGER": ledger}, "whatsapp-sender")
	h := waHarness(t, defs, "whatsapp-sender")
	chat := "120363000000000001@g.us"

	// auto: sent once, with the quote-reply on the envelope.
	id := h.start("whatsapp-sender", map[string]any{"chat": chat, "text": "pong", "replyTo": "M1", "replyToSender": "1555@s.whatsapp.net"})
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("auto: %s %s %v", run.Status, run.Error, stepOut(t, h, id, "deliver"))
	}
	if res := senderResult(t, h, id); res["status"] != "sent" || res["id"] != "wamid-1" || res["action"] != "reply" {
		t.Fatalf("auto result %v", res)
	}
	envs := waSent(t, dir)
	if len(envs) != 1 || envs[0]["target"] != chat || envs[0]["replyTo"].(map[string]any)["id"] != "M1" {
		t.Fatalf("envelopes %v", envs)
	}

	// The same request again is not sent again.
	id = h.start("whatsapp-sender", map[string]any{"chat": chat, "text": "pong", "replyTo": "M1", "replyToSender": "1555@s.whatsapp.net"})
	h.waitEnded(id)
	if res := senderResult(t, h, id); res["status"] != "already" {
		t.Fatalf("repeat result %v", res)
	}

	// draft and none send nothing.
	for policy, want := range map[string]string{"draft": "drafted", "none": "skipped"} {
		id = h.start("whatsapp-sender", map[string]any{"chat": chat, "text": "hi " + policy, "responsePolicy": policy})
		h.waitEnded(id)
		if res := senderResult(t, h, id); res["status"] != want {
			t.Fatalf("%s result %v", policy, res)
		}
	}
	if n := len(waSent(t, dir)); n != 1 {
		t.Fatalf("draft/none sent %d messages", n-1)
	}

	// approval pauses before the send; approving sends it.
	id = h.start("whatsapp-sender", map[string]any{"chat": chat, "text": "needs a yes", "responsePolicy": "approval"})
	if run := h.wait(id); run.Status != "awaiting_approval" {
		t.Fatalf("approval: %s", run.Status)
	}
	if n := len(waSent(t, dir)); n != 1 {
		t.Fatalf("sent before approval")
	}
	if err := h.eng.Approve(context.Background(), id, "approve", Actor{ID: "owner", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	h.waitEnded(id)
	if res := senderResult(t, h, id); res["status"] != "sent" {
		t.Fatalf("approved result %v", res)
	}

	// A reaction is a reaction, not a text.
	id = h.start("whatsapp-sender", map[string]any{"chat": chat, "reactTo": "M9", "reaction": "👍"})
	h.waitEnded(id)
	envs = waSent(t, dir)
	if last := envs[len(envs)-1]; last["reaction"] == nil || last["reaction"].(map[string]any)["emoji"] != "👍" {
		t.Fatalf("reaction envelope %v", last)
	}
}

func TestWhatsAppSenderNeverResendsUncertain(t *testing.T) {
	cmd, dir := fakeWA(t, "uncertain")
	ledger := filepath.Join(t.TempDir(), "ledger.jsonl")
	defs := loadWATemplates(t, map[string]string{"WA_ADAPTER": cmd, "WA_SENDER_LEDGER": ledger}, "whatsapp-sender")
	h := waHarness(t, defs, "whatsapp-sender")
	in := map[string]any{"chat": "1555@s.whatsapp.net", "text": "maybe"}
	id := h.start("whatsapp-sender", in)
	if run := h.waitEnded(id); run.Status != "failed" || !strings.Contains(run.Error, "UNCERTAIN") {
		t.Fatalf("uncertain: %s %s", run.Status, run.Error)
	}
	id = h.start("whatsapp-sender", in)
	h.waitEnded(id)
	if res := senderResult(t, h, id); res["status"] != "already" {
		t.Fatalf("retry result %v", res)
	}
	if n := len(waSent(t, dir)); n != 1 {
		t.Fatalf("an uncertain send was attempted %d times", n)
	}
}

// pingpong end to end: reply through whatsapp-sender, and the second message
// in the same chat remembers the first (memory is per conversation).
func TestPingpongRepliesThroughTheSenderAndRemembers(t *testing.T) {
	cmd, dir := fakeWA(t, "sent")
	env := map[string]string{"WA_ADAPTER": cmd, "WA_SENDER_LEDGER": filepath.Join(t.TempDir(), "l.jsonl")}
	defs := loadWATemplates(t, env, "pingpong", "whatsapp-sender")
	h := waHarness(t, defs, "pingpong")
	chat := uniq("group") + "@g.us"
	msg := func(id, text string) map[string]any {
		return map[string]any{
			"message":      map[string]any{"channel": "wa", "chat": chat, "id": id, "from": "1555@s.whatsapp.net", "text": text, "kind": "message"},
			"subscription": map[string]any{"name": "pingpong-test", "responsePolicy": "auto"},
		}
	}
	for i, want := range []string{"pong #1", "pong #2"} {
		id := h.start("pingpong", msg(uniq("M"), "ping"))
		if run := h.waitEnded(id); run.Status != "done" {
			t.Fatalf("run %d: %s %s", i, run.Status, run.Error)
		}
		save := stepOut(t, h, id, "save")
		if !strings.Contains(asStr(save["stdout"]), `"said": "`+want+`"`) || !strings.Contains(asStr(save["stdout"]), `"status": "sent"`) {
			t.Fatalf("run %d saved %v", i, save)
		}
	}
	envs := waSent(t, dir)
	if len(envs) != 2 || envs[1]["text"] != "pong #2" || envs[1]["target"] != chat {
		t.Fatalf("sent %v", envs)
	}
	// A reaction is not answered.
	in := msg(uniq("M"), "")
	in["message"].(map[string]any)["kind"] = "reaction"
	id := h.start("pingpong", in)
	if run := h.waitEnded(id); run.Status != "done" || len(waSent(t, dir)) != 2 {
		t.Fatalf("reaction: %s %s", run.Status, run.Error)
	}
}

// router: classifies, starts the owner as a child run with the event passed
// through as an OBJECT, and routes nothing it has no rule for.
func TestRouterStartsTheMatchedWorkflow(t *testing.T) {
	cmd, dir := fakeWA(t, "sent")
	env := map[string]string{"WA_ADAPTER": cmd, "WA_SENDER_LEDGER": filepath.Join(t.TempDir(), "l.jsonl")}
	defs := loadWATemplates(t, env, "router", "pingpong", "whatsapp-sender")
	h := waHarness(t, defs, "router")
	chat := uniq("g") + "@g.us"
	id := h.start("router", map[string]any{
		"event":        map[string]any{"channel": "wa", "chat": chat, "id": "R1", "from": "1555@s.whatsapp.net", "text": "ping please", "kind": "message"},
		"subscription": map[string]any{"name": "all", "responsePolicy": "auto"},
	})
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("router: %s %s", run.Status, run.Error)
	}
	kids, _ := h.store.ChildRuns(context.Background(), id)
	if len(kids) != 1 || kids[0].Workflow != "pingpong" {
		t.Fatalf("children %+v", kids)
	}
	if child := h.waitEnded(kids[0].ID); child.Status != "done" {
		t.Fatalf("pingpong child: %s %s", child.Status, child.Error)
	}
	if envs := waSent(t, dir); len(envs) != 1 || envs[0]["target"] != chat {
		t.Fatalf("sent %v", envs)
	}

	id = h.start("router", map[string]any{"event": map[string]any{"chat": chat, "kind": "reaction"}})
	run := h.waitEnded(id)
	kids, _ = h.store.ChildRuns(context.Background(), id)
	if run.Status != "done" || len(kids) != 0 || !strings.Contains(run.Outcome, "no route") {
		t.Fatalf("no-route: %s %q %d children", run.Status, run.Outcome, len(kids))
	}
}

// fakeWFXAPI is the slice of /api the subscription manager and the monitor read.
type fakeWFXAPI struct {
	mu       sync.Mutex
	subs     map[string]map[string]any
	channels []map[string]any
	messages []map[string]any
}

func (f *fakeWFXAPI) serve(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/api")
		parts := strings.Split(strings.Trim(p, "/"), "/")
		write := func(code int, v any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }
		switch {
		case p == "/channels":
			write(200, map[string]any{"channels": f.channels})
		case p == "/channels/messages":
			write(200, f.messages)
		case p == "/subscriptions":
			list := []any{}
			for _, s := range f.subs {
				list = append(list, s)
			}
			write(200, list)
		case parts[0] == "subscriptions" && len(parts) == 2:
			name := parts[1]
			switch r.Method {
			case http.MethodGet:
				if s, ok := f.subs[name]; ok {
					write(200, map[string]any{"subscription": s, "deliveries": []any{}})
				} else {
					write(404, map[string]any{"error": "no such subscription"})
				}
			case http.MethodPut:
				var s map[string]any
				_ = json.NewDecoder(r.Body).Decode(&s)
				s["name"], s["status"] = name, "active"
				f.subs[name] = s
				write(200, s)
			case http.MethodDelete:
				delete(f.subs, name)
				write(200, map[string]any{"ok": true})
			}
		case parts[0] == "subscriptions" && len(parts) == 3:
			s, ok := f.subs[parts[1]]
			if !ok {
				write(404, map[string]any{"error": "no subscription"})
				return
			}
			s["status"] = map[string]string{"pause": "paused", "resume": "active", "validate": "active"}[parts[2]]
			write(200, s)
		default:
			write(404, map[string]any{"error": "not here"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSubscriptionManagerChangesAndReadsBack(t *testing.T) {
	api := &fakeWFXAPI{subs: map[string]map[string]any{}}
	defs := loadWATemplates(t, map[string]string{"WFX_API": api.serve(t)}, "whatsapp-subscription-manager")
	h := waHarness(t, defs, "whatsapp-subscription-manager")
	wf := "whatsapp-subscription-manager"
	apply := func(id uuid.UUID) map[string]any {
		var res map[string]any
		_ = json.Unmarshal([]byte(strings.TrimSpace(asStr(stepOut(t, h, id, "apply")["stdout"]))), &res)
		return res
	}

	id := h.start(wf, map[string]any{"action": "add", "name": "pp", "channel": "wa", "chat": "g1@g.us", "workflow": "pingpong", "allow": "1555@s.whatsapp.net", "responsePolicy": "auto"})
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("add: %s %s", run.Status, run.Error)
	}
	if res := apply(id); res["verified"] != true || res["status"] != "active" {
		t.Fatalf("add result %v", res)
	}
	if allow := api.subs["pp"]["allow"].([]any); len(allow) != 1 {
		t.Fatalf("allow not sent as a list: %v", api.subs["pp"])
	}

	id = h.start(wf, map[string]any{"action": "pause", "name": "pp"})
	h.waitEnded(id)
	if res := apply(id); res["status"] != "paused" || res["verified"] != true {
		t.Fatalf("pause result %v", res)
	}

	// add without allow is refused — empty means nobody, say so.
	id = h.start(wf, map[string]any{"action": "add", "name": "x", "channel": "wa", "chat": "g@g.us", "workflow": "pingpong"})
	if run := h.waitEnded(id); run.Status != "failed" {
		t.Fatalf("add without allow: %s", run.Status)
	}

	// rm waits for a person.
	id = h.start(wf, map[string]any{"action": "rm", "name": "pp"})
	if run := h.wait(id); run.Status != "awaiting_approval" {
		t.Fatalf("rm: %s", run.Status)
	}
	if _, still := api.subs["pp"]; !still {
		t.Fatal("removed before approval")
	}
	if err := h.eng.Approve(context.Background(), id, "approve", Actor{ID: "owner", Via: "test"}); err != nil {
		t.Fatal(err)
	}
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("rm after approval: %s %s", run.Status, run.Error)
	}
	if _, still := api.subs["pp"]; still {
		t.Fatal("not removed")
	}
}

func TestWhatsAppMonitorAlertsWhenDeaf(t *testing.T) {
	now := time.Now().UTC()
	api := &fakeWFXAPI{
		subs: map[string]map[string]any{
			"broken": {"name": "broken", "channel": "wa", "status": "error", "error": "workflow gone"},
			"groups": {"name": "groups", "channel": "wa", "status": "active", "warning": "the channel's own ids (self) are unknown"},
		},
		channels: []map[string]any{{"name": "wa", "lastReceive": now.Format(time.RFC3339)}},
		messages: []map[string]any{{"at": now.Add(-2 * time.Hour).Format(time.RFC3339)}},
	}
	probe := filepath.Join(t.TempDir(), "probe.sh")
	_ = os.WriteFile(probe, []byte(`echo '{"latest": "`+now.Format(time.RFC3339)+`"}'`), 0o600)
	cmd, dir := fakeWA(t, "sent")
	env := map[string]string{
		"WFX_API": api.serve(t), "WA_CHANNEL": "wa", "STORE_PROBE": "sh " + probe,
		"WA_ADAPTER": cmd, "WA_SENDER_LEDGER": filepath.Join(t.TempDir(), "l.jsonl"),
	}
	defs := loadWATemplates(t, env, "whatsapp-monitor", "whatsapp-sender")
	h := waHarness(t, defs, "whatsapp-monitor")
	for i := 0; i < 2; i++ {
		id := h.start("whatsapp-monitor", map[string]any{"owner_chat": "owner@s.whatsapp.net", "dry_run": false})
		if run := h.waitEnded(id); run.Status != "done" {
			t.Fatalf("monitor %d: %s %s %v", i, run.Status, run.Error, stepOut(t, h, id, "check"))
		}
	}
	envs := waSent(t, dir)
	if len(envs) != 1 {
		t.Fatalf("want ONE alert for the same findings in the window, got %d", len(envs))
	}
	text := asStr(envs[0]["text"])
	for _, want := range []string{"deaf", "subscription broken", "unknown-self groups"} {
		if !strings.Contains(text, want) {
			t.Fatalf("alert lacks %q:\n%s", want, text)
		}
	}
	if envs[0]["target"] != "owner@s.whatsapp.net" {
		t.Fatalf("alert went to %v", envs[0]["target"])
	}

	// Healthy: no alert.
	api.mu.Lock()
	api.subs = map[string]map[string]any{}
	api.messages = []map[string]any{{"at": now.Format(time.RFC3339)}}
	api.mu.Unlock()
	id := h.start("whatsapp-monitor", map[string]any{"owner_chat": "owner@s.whatsapp.net"})
	if run := h.waitEnded(id); run.Status != "done" || len(waSent(t, dir)) != 1 {
		t.Fatalf("healthy: %s %s", run.Status, run.Error)
	}
}

// Every default workflow, frontdesk's agent step included, dry-runs clean.
func TestWhatsAppTemplatesDryRun(t *testing.T) {
	names := []string{"whatsapp-sender", "whatsapp-monitor", "whatsapp-subscription-manager", "router", "pingpong", "frontdesk", "load-context", "retrospective"}
	defs := loadWATemplates(t, nil, names...)
	if fd := defs["frontdesk"]; len(fd.Steps) == 0 || fd.Steps[0].ID != "ctx.gather" {
		t.Fatalf("frontdesk must embed load-context first, got %v", fd.Steps[0].ID)
	}
	// load-context's typed inputs (#82) resolve to their defaults in both forms.
	for _, n := range []string{"frontdesk", "load-context"} {
		run := defs[n].Steps[0].Run
		if strings.Contains(run, ".Inputs.") || !strings.Contains(run, `open("context.json", "w")`) || !strings.Contains(run, `or "auto"`) {
			t.Fatalf("%s: load-context inputs not substituted:\n%s", n, run)
		}
	}
	h := waHarness(t, defs, "frontdesk")
	msg := map[string]any{"channel": "wa", "chat": "g@g.us", "id": "M1", "from": "1555@s.whatsapp.net", "text": "hi", "kind": "message"}
	inputs := map[string]map[string]any{
		"whatsapp-sender":               {"chat": "g@g.us", "text": "hi"},
		"whatsapp-monitor":              {"owner_chat": "owner@s.whatsapp.net"},
		"whatsapp-subscription-manager": {"action": "ls"},
		"router":                        {"event": msg},
		"pingpong":                      {"message": msg},
		"frontdesk":                     {"message": msg},
		"load-context":                  {"message": msg},
		"retrospective":                 {},
	}
	for _, n := range names {
		rep, err := h.eng.DryRunWorkflow(n, inputs[n])
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		for _, p := range rep.Problems {
			if p.Fatal {
				t.Errorf("%s: %s", n, p.Message)
			} else {
				t.Logf("%s (warning): %s", n, p.Message)
			}
		}
		if !rep.OK {
			t.Errorf("%s: dry run not OK", n)
		}
	}
}

// frontdesk's decision: confident → reply; an allow-listed workflow → hand
// off; anything else (low confidence, a workflow not on the list) → a person.
func TestFrontdeskDecides(t *testing.T) {
	script, _ := filepath.Abs(repoPath(t, "templates/frontdesk/decide.py"))
	cases := []struct {
		answer string
		code   int
		out    string
	}{
		{`{"reply":"Open 9-5","confidence":0.9,"handoff_to":"none","reason":"in instructions"}`, 0, "reply"},
		{`{"reply":"","confidence":0.9,"handoff_to":"workflow","handoff_workflow":"refunds","reason":"refund"}`, 10, "refunds"},
		{`{"reply":"","confidence":0.9,"handoff_to":"workflow","handoff_workflow":"rm-rf","reason":"x"}`, 11, "person"},
		{`{"reply":"maybe","confidence":0.3,"handoff_to":"none","reason":"unsure"}`, 11, "person"},
		{`{"reply":"x"}`, 2, "malformed"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "answer.json"), []byte(c.answer), 0o600)
		cmd := exec.Command("python3", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HANDOFF_WORKFLOWS=refunds,pingpong", "MIN_CONFIDENCE=0.7")
		out, _ := cmd.Output()
		if cmd.ProcessState.ExitCode() != c.code || !strings.Contains(string(out), c.out) {
			t.Errorf("%s → exit %d %q, want %d %q", c.answer, cmd.ProcessState.ExitCode(), out, c.code, c.out)
		}
	}
}
