package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/supervisor"
	"github.com/muthuishere/wfnexus/apps/api/internal/wamonitor"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The preinstalled apl module end to end (#109): a default profile set once,
// the whatsapp service started from it, and apl.whatsapp-send called three
// ways — `wfx module call` (POST /api/modules/{name}/call) and `uses:` in a
// workflow — with every send going out through the running service's outbox
// (a fake adapter: nothing reaches WhatsApp).
func TestAplWhatsappSendGoesThroughTheRunningService(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	dbPath := filepath.Join(tmp, "wfnexus.db")
	if err := store.Migrate("sqlite", dbPath); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	modulesDir, _ := filepath.Abs("../../../../modules")
	cfg := config.Config{Addr: addr, WorkDir: filepath.Join(tmp, "work"), WorkflowsDir: filepath.Join(tmp, "workflows"),
		SkillsDir: filepath.Join(tmp, "skills"), ModulesDir: modulesDir, MaxConcurrentRuns: 4}
	for _, d := range []string{cfg.WorkDir, cfg.WorkflowsDir, cfg.SkillsDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	// A workflow that sends with `uses:`.
	_ = os.WriteFile(filepath.Join(cfg.WorkflowsDir, "notify-ops.yaml"), []byte(`name: notify-ops
on: workflow_dispatch
steps:
  - id: tell
    uses: apl.whatsapp-send@0
    with: { chat: ops@g.us, text: deployed, responsePolicy: auto }
`), 0o644)
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(cfg.SkillsDir), nil)
	if err := eng.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	h := New(eng, st, nil, addr, "", nil)
	srv := httptest.NewUnstartedServer(h)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	if eng.Definitions()["notify-ops"] == nil || eng.Definitions()["apl.whatsapp-send"] == nil {
		t.Fatalf("definitions: notify-ops=%v call wrapper=%v", eng.Definitions()["notify-ops"] != nil, eng.Definitions()["apl.whatsapp-send"] != nil)
	}

	// Profile once, globally; the service starts from the default profile.
	if w := do(h, "PUT", "/api/modules/apl/profiles/business", "", map[string]any{"account": "biz", "default": true}); w.Code != 200 {
		t.Fatalf("profile: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/modules/apl.whatsapp/services", "", map[string]any{}); w.Code != 201 || !strings.Contains(w.Body.String(), `"account":"biz"`) {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}

	// The running service: the real monitor's outbox drain with a fake sender.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	specs, _ := eng.ServicesFor(ctx, "server", nil, true)
	if len(specs) != 1 || specs[0].Key != "apl.whatsapp/biz" {
		t.Fatalf("specs = %+v", specs)
	}
	self := &supervisor.Self{URL: specs[0].Env[supervisor.EnvURL], Key: specs[0].Key, Token: specs[0].Env[supervisor.EnvToken]}
	sent := filepath.Join(tmp, "sent.jsonl")
	fake := filepath.Join(tmp, "fake-send.py")
	_ = os.WriteFile(fake, []byte("import json, sys\nenv = json.load(sys.stdin)\nf = open(sys.argv[1], 'a'); f.write(json.dumps(env) + '\\n'); f.close()\nn = sum(1 for _ in open(sys.argv[1]))\nprint(json.dumps({'id': 'wamid-%d' % n, 'status': 'sent'}))\n"), 0o600)
	m, err := wamonitor.New(wamonitor.Config{
		Receive: []string{"true"}, StateFile: filepath.Join(tmp, "mon.json"), Channel: "whatsapp",
		Watches: func(context.Context) ([]wamonitor.Watch, error) { return nil, nil },
		Outbox:  self.Outbox, Report: self.Report, Send: []string{"python3", fake, sent}, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for ctx.Err() == nil {
			m.Drain(ctx)
			time.Sleep(100 * time.Millisecond)
		}
	}()
	sends := func() []map[string]any {
		raw, _ := os.ReadFile(sent)
		var out []map[string]any
		for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var e map[string]any
			if json.Unmarshal([]byte(l), &e) == nil {
				out = append(out, e)
			}
		}
		return out
	}
	callSend := func(inputs map[string]any) (string, string) {
		t.Helper()
		w := do(h, "POST", "/api/modules/apl.whatsapp-send/call", "", map[string]any{"inputs": inputs, "wait": true})
		if w.Code != 201 {
			t.Fatalf("call: %d %s", w.Code, w.Body)
		}
		var res struct {
			Run struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"run"`
			Output struct {
				Stdout string `json:"stdout"`
			} `json:"output"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return res.Run.Status + " " + res.Run.Error, res.Output.Stdout
	}

	// No policy: drafted, nothing queued or sent.
	if status, out := callSend(map[string]any{"chat": "g1@g.us", "text": "hi"}); !strings.HasPrefix(status, "done") || !strings.Contains(out, `"status": "drafted"`) {
		t.Fatalf("draft: %s %s", status, out)
	}
	if len(sends()) != 0 {
		t.Fatalf("a draft was sent: %v", sends())
	}
	// auto: queued on the running service, sent by it, reported back.
	if status, out := callSend(map[string]any{"chat": "g1@g.us", "text": "hi", "responsePolicy": "auto"}); !strings.HasPrefix(status, "done") || !strings.Contains(out, `"status": "sent"`) {
		t.Fatalf("auto: %s %s", status, out)
	}
	if s := sends(); len(s) != 1 || s[0]["target"] != "g1@g.us" || s[0]["text"] != "hi" {
		t.Fatalf("sent %v", s)
	}
	// The same request again is the same key: answered, not sent twice.
	if status, out := callSend(map[string]any{"chat": "g1@g.us", "text": "hi", "responsePolicy": "auto"}); !strings.HasPrefix(status, "done") || !strings.Contains(out, `"status": "sent"`) {
		t.Fatalf("repeat: %s %s", status, out)
	}
	if len(sends()) != 1 {
		t.Fatalf("a repeat was sent again: %v", sends())
	}

	// `uses: apl.whatsapp-send` in a workflow.
	w := do(h, "POST", "/api/workflows/notify-ops/runs", "", map[string]any{})
	if w.Code != 201 {
		t.Fatalf("notify-ops: %d %s", w.Code, w.Body)
	}
	var created struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if s := sends(); len(s) == 2 && s[1]["target"] == "ops@g.us" && s[1]["text"] == "deployed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("uses: sends %v", sends())
		}
		time.Sleep(100 * time.Millisecond)
	}

	// A profile whose account has no running service: refused, nothing sent.
	if w := do(h, "PUT", "/api/modules/apl/profiles/personal", "", map[string]any{"account": "me"}); w.Code != 200 {
		t.Fatalf("profile: %d %s", w.Code, w.Body)
	}
	if status, _ := callSend(map[string]any{"chat": "g2@g.us", "text": "x", "responsePolicy": "auto", "profile": "personal"}); !strings.HasPrefix(status, "failed") {
		t.Fatalf("no service: %s", status)
	}
	if len(sends()) != 2 {
		t.Fatalf("sent without a service: %v", sends())
	}
	// The module token opens its own door only.
	if w := do(h, "POST", "/api/module-self/apl/outbox", "wfm_nope", map[string]any{}); w.Code != 401 {
		t.Fatalf("bad module token: %d", w.Code)
	}
}
