package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// THE DEFAULT ENGINE AGENTS: retrospective (#94) and the engine frontdesk
// (#95). Their scripts run for real against a FAKE /api; the agent steps are
// covered by the dry run, so no model is called.

// fakeEngineAPI serves /api/workflows, /api/runs and /api/runs/{id} from fixed
// data, and records every method it saw (both agents must only GET).
func fakeEngineAPI(t *testing.T, runs []map[string]any, steps map[string][]map[string]any) (string, *[]string) {
	t.Helper()
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/api")
		switch {
		case p == "/workflows":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "deploy", "description": "ships the app", "on": map[string]any{"workflow_dispatch": map[string]any{}}},
				{"name": "nightly", "description": "nightly checks"},
			})
		case p == "/runs":
			out := []map[string]any{}
			for _, x := range runs {
				if wf := r.URL.Query().Get("workflow"); wf == "" || x["workflow"] == wf {
					out = append(out, x)
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(p, "/runs/"):
			id := strings.TrimPrefix(p, "/runs/")
			for _, x := range runs {
				if x["id"] == id {
					st := steps[id]
					if st == nil {
						st = []map[string]any{}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"run": x, "steps": st})
					return
				}
			}
			w.WriteHeader(404)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &methods
}

// runScript runs a template's python script in dir with env, and returns its
// exit code and stdout.
func runScript(t *testing.T, script, dir string, env ...string) (int, string) {
	t.Helper()
	abs, _ := filepath.Abs(repoPath(t, script))
	cmd := exec.Command("python3", abs)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("%s: %v", script, err)
		}
	}
	return cmd.ProcessState.ExitCode(), string(out)
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// retrospective: collect reads failures, retries, approval waits and cost, and
// tracks whether the previous retrospective's findings held; check keeps only
// findings that cite real evidence.
func TestRetrospectiveCollectsTracksAndChecks(t *testing.T) {
	now := time.Now().UTC()
	at := func(h float64) string { return now.Add(time.Duration(-h * float64(time.Hour))).Format(time.RFC3339) }
	prevOut, _ := json.Marshal(map[string]any{"findings": []map[string]any{
		{"workflow": "deploy", "what": "deploy fails on a missing token", "fix": "wfx env set DEPLOY_TOKEN"},
		{"workflow": "nightly", "what": "nightly timed out", "fix": "timeout_sec: 600"},
	}, "summary": "two"})
	runs := []map[string]any{
		{"id": "retro-1", "workflow": "retrospective", "status": "done", "createdAt": at(20)},
		{"id": "r-dep", "workflow": "deploy", "status": "failed", "error": "DEPLOY_TOKEN missing", "currentStep": "push", "createdAt": at(2)},
		{"id": "r-night", "workflow": "nightly", "status": "done", "createdAt": at(3)},
		{"id": "r-appr", "workflow": "deploy", "status": "awaiting_approval", "currentStep": "gate", "createdAt": at(5)},
		{"id": "r-old", "workflow": "deploy", "status": "failed", "createdAt": at(200)},
	}
	steps := map[string][]map[string]any{
		"retro-1": {{"stepId": "check", "status": "done", "output": map[string]any{"stdout": string(prevOut)}}},
		"r-dep": {
			{"stepId": "build", "status": "done", "attempts": 3, "usage": map[string]any{"totalTokens": 1200, "costUsd": 0.02}},
			{"stepId": "push", "status": "failed", "attempts": 1, "error": "DEPLOY_TOKEN missing"},
		},
		"r-appr": {{"stepId": "gate", "status": "awaiting_approval", "startedAt": at(5)}},
		"r-night": {{"stepId": "check", "status": "done", "resolution": "approved", "startedAt": at(3), "resolvedAt": at(1)}},
	}
	url, methods := fakeEngineAPI(t, runs, steps)
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "input.json"), map[string]any{})
	code, out := runScript(t, "templates/retrospective/collect.py", dir, "WFX_API="+url, "WFX_RUN_ID=retro-2")
	if code != 0 {
		t.Fatalf("collect: exit %d %s", code, out)
	}
	facts := readJSON(t, filepath.Join(dir, "facts.json"))
	per := facts["runs"].(map[string]any)
	dep := per["deploy"].(map[string]any)
	if dep["total"].(float64) != 2 || dep["retries"].(float64) != 2 || dep["tokens"].(float64) != 1200 || dep["costUsd"].(float64) != 0.02 {
		t.Fatalf("deploy stats %v", dep)
	}
	if _, old := per["retrospective"]; old {
		t.Fatal("the retrospective reviewed itself")
	}
	if f := facts["failures"].([]any); len(f) != 1 || f[0].(map[string]any)["stepId"] != "push" {
		t.Fatalf("failures %v (the 200h-old run is outside the window)", f)
	}
	if w := facts["waits"].([]any); len(w) != 2 {
		t.Fatalf("waits %v", w)
	}
	status := map[string]string{}
	for _, p := range facts["previous"].(map[string]any)["findings"].([]any) {
		m := p.(map[string]any)
		status[m["workflow"].(string)] = m["status"].(string)
	}
	if status["deploy"] != "recurred" || status["nightly"] != "no new failure" {
		t.Fatalf("fix tracking %v", status)
	}
	for _, m := range *methods {
		if m != http.MethodGet {
			t.Fatalf("retrospective sent a %s — it is read-only", m)
		}
	}

	// check: an invented run and an unknown workflow are dropped.
	writeJSON(t, filepath.Join(dir, "analysis.json"), map[string]any{
		"summary": "deploy still fails; the token fix did not hold",
		"findings": []map[string]any{
			{"workflow": "deploy", "runId": "r-dep", "stepId": "push", "what": "push fails", "why": "DEPLOY_TOKEN missing", "fix": "wfx env set DEPLOY_TOKEN"},
			{"workflow": "deploy", "runId": "r-made-up", "what": "x", "why": "y", "fix": "z"},
			{"workflow": "ghost", "what": "x", "why": "y", "fix": "z"},
			{"workflow": "deploy", "runId": "r-dep", "stepId": "build", "what": "build retried twice", "why": "flaky", "fix": "retry: 1"},
		},
	})
	code, out = runScript(t, "templates/retrospective/check.py", dir)
	if code != 0 {
		t.Fatalf("check: exit %d %s", code, out)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("check output %q: %v", out, err)
	}
	if k, d := res["findings"].([]any), res["dropped"].([]any); len(k) != 2 || len(d) != 2 {
		t.Fatalf("kept %v dropped %v", k, d)
	}
	if tr := res["fixTracking"].([]any); len(tr) != 2 {
		t.Fatalf("fixTracking %v", tr)
	}

	// A malformed analysis fails; an unreadable API fails.
	_ = os.WriteFile(filepath.Join(dir, "analysis.json"), []byte(`{"summary":"x"}`), 0o600)
	if code, _ := runScript(t, "templates/retrospective/check.py", dir); code != 2 {
		t.Fatalf("malformed analysis: exit %d", code)
	}
	if code, _ := runScript(t, "templates/retrospective/collect.py", dir, "WFX_API=http://127.0.0.1:1"); code != 2 {
		t.Fatalf("unreadable API: exit %d", code)
	}
}

// frontdesk's engine snapshot: workflows, failures and what waits on a person,
// GET only; the question comes from the chat message or the manual `question`.
func TestFrontdeskEngineSnapshot(t *testing.T) {
	runs := []map[string]any{
		{"id": "a", "workflow": "deploy", "status": "failed", "error": "boom", "createdAt": "2026-11-07T10:00:00Z"},
		{"id": "b", "workflow": "deploy", "status": "awaiting_approval", "currentStep": "gate", "createdAt": "2026-11-07T11:00:00Z"},
		{"id": "c", "workflow": "nightly", "status": "done", "createdAt": "2026-11-07T09:00:00Z"},
	}
	url, methods := fakeEngineAPI(t, runs, nil)
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "input.json"), map[string]any{"question": "what is waiting on me?"})
	if code, out := runScript(t, "templates/frontdesk/engine.py", dir, "WFX_API="+url); code != 0 {
		t.Fatalf("engine: %d %s", code, out)
	}
	s := readJSON(t, filepath.Join(dir, "engine.json"))
	if s["question"] != "what is waiting on me?" || len(s["workflows"].([]any)) != 2 {
		t.Fatalf("snapshot %v", s)
	}
	if f := s["failed"].([]any); len(f) != 1 || f[0].(map[string]any)["id"] != "a" {
		t.Fatalf("failed %v", f)
	}
	if w := s["waiting"].([]any); len(w) != 1 || w[0].(map[string]any)["step"] != "gate" {
		t.Fatalf("waiting %v", w)
	}
	if recent := s["recent"].([]any); recent[0].(map[string]any)["id"] != "b" {
		t.Fatalf("recent not newest first: %v", recent)
	}
	for _, m := range *methods {
		if m != http.MethodGet {
			t.Fatalf("frontdesk sent a %s — it is read-only", m)
		}
	}

	// An engine it cannot read is said, not guessed.
	writeJSON(t, filepath.Join(dir, "input.json"), map[string]any{"message": map[string]any{"text": "status?"}})
	if code, _ := runScript(t, "templates/frontdesk/engine.py", dir, "WFX_API=http://127.0.0.1:1"); code != 0 {
		t.Fatalf("engine must not fail the run")
	}
	if s := readJSON(t, filepath.Join(dir, "engine.json")); s["error"] == nil || s["question"] != "status?" {
		t.Fatalf("unreadable engine snapshot %v", s)
	}
}

// The new decisions: "why did X fail" → retrospective (12) from a chat or a
// manual run; no chat → answered in the run (20), never a send or an approval.
func TestFrontdeskDecidesForTheEngine(t *testing.T) {
	cases := []struct {
		chat   string
		answer string
		code   int
		out    string
	}{
		{"g@g.us", `{"reply":"","confidence":0.9,"handoff_to":"diagnose","diagnose_workflow":"deploy","reason":"why"}`, 12, "retrospective"},
		{"", `{"reply":"","confidence":0.9,"handoff_to":"diagnose","diagnose_workflow":"deploy","reason":"why"}`, 12, "retrospective"},
		{"", `{"reply":"2 runs wait on you","confidence":0.9,"handoff_to":"none","reason":"engine.json"}`, 20, "answered"},
		{"", `{"reply":"unsure","confidence":0.2,"handoff_to":"human","reason":"approve it for me"}`, 20, "answered"},
		{"g@g.us", `{"reply":"2 runs wait","confidence":0.9,"handoff_to":"none","reason":"engine.json"}`, 0, "reply"},
		{"g@g.us", `{"reply":"","confidence":0.9,"handoff_to":"human","reason":"approve it for me"}`, 11, "person"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "answer.json"), []byte(c.answer), 0o600)
		writeJSON(t, filepath.Join(dir, "context.json"), map[string]any{"chat": c.chat})
		code, out := runScript(t, "templates/frontdesk/decide.py", dir, "HANDOFF_WORKFLOWS=pingpong", "MIN_CONFIDENCE=0.7")
		if code != c.code || !strings.Contains(out, c.out) {
			t.Errorf("chat=%q %s → exit %d %q, want %d %q", c.chat, c.answer, code, out, c.code, c.out)
		}
	}
}

// explain: retrospective's output (its check step) becomes a cited reply.
func TestFrontdeskExplainsTheDiagnosis(t *testing.T) {
	stdout, _ := json.Marshal(map[string]any{"summary": "deploy fails on a missing token", "findings": []map[string]any{
		{"workflow": "deploy", "runId": "r-dep", "stepId": "push", "what": "push fails", "why": "DEPLOY_TOKEN missing", "fix": "wfx env set DEPLOY_TOKEN"},
	}})
	for chat, want := range map[string]int{"g@g.us": 0, "": 20} {
		dir := t.TempDir()
		writeJSON(t, filepath.Join(dir, "diagnosis.json"), map[string]any{"ok": true, "exitCode": 0, "stdout": string(stdout)})
		writeJSON(t, filepath.Join(dir, "context.json"), map[string]any{"chat": chat})
		code, out := runScript(t, "templates/frontdesk/explain.py", dir)
		if code != want || !strings.Contains(out, "deploy / r-dep / push") || !strings.Contains(out, "wfx env set DEPLOY_TOKEN") {
			t.Errorf("chat=%q: exit %d %q", chat, code, out)
		}
	}
}

// The engine frontdesk dry-runs from a manual question too, with retrospective beside it.
func TestEngineFrontdeskDryRunFromAQuestion(t *testing.T) {
	defs := loadWATemplates(t, nil, "frontdesk", "load-context", "retrospective", "whatsapp-sender", "pingpong")
	h := waHarness(t, defs, "frontdesk")
	for _, n := range []string{"frontdesk", "retrospective"} {
		in := map[string]any{"question": "why did deploy fail?"}
		if n == "retrospective" {
			in = map[string]any{"workflow": "deploy"}
		}
		rep, err := h.eng.DryRunWorkflow(n, in)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		for _, p := range rep.Problems {
			if p.Fatal {
				t.Errorf("%s: %s", n, p.Message)
			}
		}
		if !rep.OK {
			t.Errorf("%s: dry run not OK", n)
		}
	}
	fd := defs["frontdesk"]
	var hasDiag bool
	for _, s := range fd.Steps {
		if s.ID == "diagnose" && s.Start != nil && len(s.Start.Targets) == 1 && s.Start.Targets[0].Workflow == "retrospective" && s.Start.Mode() == "wait" {
			hasDiag = true
		}
		if len(s.Tools) > 0 && s.ID == "answer" && (len(s.Tools) != 1 || s.Tools[0] != "read") {
			t.Errorf("frontdesk's agent must be read-only, has %v", s.Tools)
		}
	}
	if !hasDiag {
		t.Error("frontdesk must delegate to retrospective with start: wait: true")
	}
	for _, s := range defs["retrospective"].Steps {
		if s.ID == "analyze" && (len(s.Tools) != 1 || s.Tools[0] != "read") {
			t.Errorf("retrospective's agent must be read-only, has %v", s.Tools)
		}
	}
}
