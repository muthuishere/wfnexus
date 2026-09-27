package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A step's `classifier:` used to be validated at load and then ignored: every
// decide ran on the process default. This asks through a step naming a
// registry entry and checks what model actually went over the wire.
func TestAStepsClassifierIsTheOneThatRuns(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		asked = append(asked, body.Model)
		mu.Unlock()
		http.Error(w, "recorded", http.StatusTeapot)
	}))
	defer srv.Close()

	dir := t.TempDir()
	reg := filepath.Join(dir, "registries.json")
	raw := `{"classifiers": {"jev-next": {"backend": "typesafe", "model": "jev-next-9", "baseUrl": "` + srv.URL + `", "apiKeyEnv": "WFX_TEST_JUDGE_KEY"}}}`
	if err := os.WriteFile(reg, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WFX_TEST_JUDGE_KEY", "NOT_A_REAL_KEY")
	cat, err := catalog.Load(reg, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{WorkDir: dir, ClassifierBaseURL: srv.URL, ClassifierModel: "the-process-default", ClassifierAPIKeyEnv: "WFX_TEST_JUDGE_KEY"}
	eng := New(cfg, nil, nil, map[string]*workflow.Definition{}, skills.Load(dir), cat)

	q := map[string]tn.Question{"x": tn.NoulQuestion{Instructions: "is it?"}}
	for _, step := range []*workflow.Step{{ID: "named", Classifier: "jev-next"}, {ID: "unnamed"}} {
		c, err := eng.classifierFor(step)
		if err != nil {
			t.Fatalf("%s: %v", step.ID, err)
		}
		_, _ = c.Evaluate(context.Background(), "state", q) // the teapot fails it; the request is what matters
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) < 2 || asked[0] != "jev-next-9" {
		t.Fatalf("the step naming jev-next asked for %v, want jev-next-9 first", asked)
	}
	if asked[len(asked)-1] != "the-process-default" {
		t.Fatalf("a step naming no classifier asked for %q, want the process default", asked[len(asked)-1])
	}

	// A backend that cannot be called here is refused by name, not swapped for the default.
	if _, err := eng.classifierFor(&workflow.Step{ID: "x", Classifier: "nope"}); err == nil {
		t.Fatal("an unknown classifier was accepted")
	}
}

// doctor's ✓ on a classifier means it could be called. A preset that implies a
// key (jev → OPENROUTER_API_KEY) used to show ready without one, and a URL
// entry whose header references an unset variable would have sent an empty
// credential.
func TestDoctorKnowsWhatAClassifierNeeds(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "registries.json")
	raw := `{"classifiers": {
	  "jev": {"backend": "openrouter", "model": "typesafe/jev-1.13"},
	  "mine": {"baseUrl": "https://jev.example.internal/v1", "headers": {"Authorization": "Bearer ${WFX_TEST_UNSET_JUDGE}"}}
	}}`
	if err := os.WriteFile(reg, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("WFX_TEST_UNSET_JUDGE", "")
	cat, err := catalog.Load(reg, "")
	if err != nil {
		t.Fatal(err)
	}
	eng := New(config.Config{WorkDir: dir}, nil, nil, map[string]*workflow.Definition{}, skills.Load(dir), cat)
	got := map[string]string{}
	for _, c := range eng.Doctor().Classifiers {
		if !c.Ready {
			got[c.Name] = c.Problem
		}
		if c.Name == "mine" && c.Kind != "url" {
			t.Errorf("a URL entry reports kind %q", c.Kind)
		}
	}
	if !strings.Contains(got["jev"], "OPENROUTER_API_KEY") {
		t.Errorf("jev without its implied key: %q", got["jev"])
	}
	if !strings.Contains(got["mine"], "WFX_TEST_UNSET_JUDGE") {
		t.Errorf("a header naming an unset variable: %q", got["mine"])
	}
}
