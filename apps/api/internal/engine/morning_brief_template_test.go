package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// morning-brief: collect (real script, fake /api, fake gh, temp NOW.md) →
// brief (scripted model) → check → whatsapp-sender on the FAKE adapter.
// Nothing reaches WhatsApp.

func morningBriefEnv(t *testing.T, ghWorks bool) map[string]string {
	t.Helper()
	at := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	url, _ := fakeEngineAPI(t, []map[string]any{
		{"id": "r-dep", "workflow": "deploy", "status": "failed", "error": "DEPLOY_TOKEN missing", "currentStep": "push", "createdAt": at},
		{"id": "r-appr", "workflow": "deploy", "status": "awaiting_approval", "currentStep": "gate", "createdAt": at},
	}, nil)
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	body := "#!/bin/sh\nif [ \"$1\" = pr ]; then echo '[{\"number\":42,\"title\":\"morning brief\",\"updatedAt\":\"2026-11-07T10:00:00Z\"}]'; else echo '[]'; fi\n"
	if !ghWorks {
		body = "#!/bin/sh\necho 'gh: not logged in' >&2; exit 1\n"
	}
	if err := os.WriteFile(gh, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	now := filepath.Join(dir, "NOW.md")
	_ = os.WriteFile(now, []byte("# NOW\n## Top\nShip morning-brief.\n## Later\nnot this\n"), 0o600)
	return map[string]string{"WFX_API": url, "GH_BIN": gh, "NOW_MD": now}
}

func TestMorningBriefDraftsByDefaultAndDropsUncitedLines(t *testing.T) {
	cmd, waDir := fakeWA(t, "sent")
	defs := loadWATemplates(t, morningBriefEnv(t, true), "morning-brief", "whatsapp-sender")
	lines := []map[string]any{
		{"text": "deploy waits on your approval", "cites": []string{"wait:r-appr"}},
		{"text": "deploy failed: DEPLOY_TOKEN missing", "cites": []string{"run:r-dep"}},
		{"text": "Review PR #42", "cites": []string{"pr:42"}},
		{"text": "Revenue doubled overnight", "cites": []string{}},   // uncited
		{"text": "The DB is down", "cites": []string{"run:made-up"}}, // invented
	}
	h := newHarness(t, defs["morning-brief"], newFakeLLM(t, submit(map[string]any{"lines": lines}), finish(),
		submit(map[string]any{"lines": lines}), finish()), "")
	h.eng.defs["whatsapp-sender"] = defs["whatsapp-sender"]
	useWAChannel(t, h, cmd, "")
	chat := uniq("owner") + "@s.whatsapp.net"

	// Default policy: draft. The run finishes, nothing is sent.
	id := h.start("morning-brief", map[string]any{"chat": chat})
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("draft run: %s %s", run.Status, run.Error)
	}
	var chk map[string]any
	if err := json.Unmarshal([]byte(asStr(stepOut(t, h, id, "check")["stdout"])), &chk); err != nil {
		t.Fatal(err)
	}
	if k, d := chk["lines"].([]any), chk["dropped"].([]any); len(k) != 3 || len(d) != 2 {
		t.Fatalf("kept %v dropped %v", k, d)
	}
	text := asStr(chk["text"])
	if strings.Contains(text, "Revenue") || strings.Contains(text, "DB is down") || !strings.Contains(text, "[pr:42]") {
		t.Fatalf("brief text %q", text)
	}
	if out := stepOut(t, h, id, "deliver"); !strings.Contains(asStr(out["stdout"])+briefJSON(out), "drafted") {
		t.Fatalf("deliver %v — want the sender's drafted result", out)
	}
	for _, e := range waSent(t, waDir) {
		if e["stepId"] == "send" {
			t.Fatalf("draft policy sent a message: %v", e)
		}
	}

	// auto: the same brief is sent once, to the chat from input.
	id = h.start("morning-brief", map[string]any{"chat": chat, "responsePolicy": "auto"})
	if run := h.waitEnded(id); run.Status != "done" {
		t.Fatalf("auto run: %s %s", run.Status, run.Error)
	}
	var sends []map[string]any
	for _, e := range waSent(t, waDir) {
		if e["stepId"] == "send" {
			sends = append(sends, e)
		}
	}
	if len(sends) != 1 || sends[0]["target"] != chat || !strings.Contains(briefJSON(sends[0]), "Review PR #42") {
		t.Fatalf("auto sends %v", sends)
	}
}

func briefJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// A missing source is noted as a citable fact, not a failure.
func TestMorningBriefCollectNotesMissingSources(t *testing.T) {
	env := morningBriefEnv(t, false)
	env["NOW_MD"] = filepath.Join(t.TempDir(), "absent.md")
	dir := t.TempDir()
	code, out := runScript(t, "templates/morning-brief/collect.py", dir,
		"WFX_API="+env["WFX_API"], "GH_BIN="+env["GH_BIN"], "NOW_MD="+env["NOW_MD"])
	if code != 0 {
		t.Fatalf("collect: exit %d %s", code, out)
	}
	ids := map[string]bool{}
	for _, f := range readJSON(t, filepath.Join(dir, "facts.json"))["facts"].([]any) {
		ids[f.(map[string]any)["id"].(string)] = true
	}
	for _, want := range []string{"run:r-dep", "wait:r-appr", "missing:github-pr", "missing:github-issue", "missing:ceo-harness"} {
		if !ids[want] {
			t.Errorf("no fact %s in %v", want, ids)
		}
	}
	// A malformed brief fails check (exit 2): nothing is delivered.
	_ = os.WriteFile(filepath.Join(dir, "brief.json"), []byte(`{"text":"x"}`), 0o600)
	if code, _ := runScript(t, "templates/morning-brief/check.py", dir); code != 2 {
		t.Fatalf("malformed brief: exit %d", code)
	}
}
