package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/judge"
)

// The recorded-session bug: `wfx judge --classifier jev` run INSIDE a step said
// "no classifier is configured", because a step does not inherit the server's
// registry path. With WFX_API set, the classifier is the SERVER's, asked over
// the API — no file path and no key cross into the step.
func TestJudgeInsideAStepAsksTheServer(t *testing.T) {
	tempContexts(t)
	var got struct {
		path, auth, classifier string
		items                  int
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Classifier string            `json:"classifier"`
			Items      []json.RawMessage `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got.path, got.auth, got.classifier, got.items = r.URL.Path, r.Header.Get("Authorization"), body.Classifier, len(body.Items)
		yes := 0.9
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []judgeResult{{
			ID: "state", Model: "jev-1.13", Calibrated: true,
			Answers: map[string]judge.Answer{"dead": {Type: "noul", Noul: &yes, Band: "yes"}},
		}}})
	}))
	defer srv.Close()
	t.Setenv("WFX_API", srv.URL)
	t.Setenv("WFX_API_TOKEN", "wfx_step_token_for_test")
	// The step has NO registry and NO key: exactly a step's environment.
	t.Setenv("WFX_REGISTRIES", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")

	q := filepath.Join(t.TempDir(), "q.yaml")
	_ = os.WriteFile(q, []byte("dead:\n  type: noul\n  instructions: Is it unreachable?\n"), 0o644)

	if err := cmdJudge([]string{"-q", q, "--state", "no callers", "--classifier", "jev"}); err != nil {
		t.Fatalf("judge inside a step: %v", err)
	}
	if got.path != "/api/judge" || got.classifier != "jev" || got.items != 1 {
		t.Fatalf("the server saw path=%q classifier=%q items=%d", got.path, got.classifier, got.items)
	}
	if got.auth != "Bearer wfx_step_token_for_test" {
		t.Fatalf("WFX_API_TOKEN was not presented (auth set=%v)", got.auth != "")
	}
}

// Anything pointing at a different endpoint keeps the in-process path, and so
// does --local.
func TestJudgeOnServerOnlyWithoutAnEndpointOverride(t *testing.T) {
	t.Setenv("WFX_JUDGE_BASE_URL", "")
	t.Setenv("WFX_API", "")
	if judgeOnServer([]string{"--classifier", "jev"}) {
		t.Fatal("no WFX_API, yet judged on a server")
	}
	t.Setenv("WFX_API", "http://127.0.0.1:1")
	if !judgeOnServer([]string{"--classifier", "jev"}) {
		t.Fatal("WFX_API set, yet not judged on the server")
	}
	for _, f := range []string{"--local", "--backend", "--url", "--model", "--key-env", "--header"} {
		if judgeOnServer([]string{"--classifier", "jev", f, "x"}) {
			t.Errorf("%s still went to the server", f)
		}
	}
}
