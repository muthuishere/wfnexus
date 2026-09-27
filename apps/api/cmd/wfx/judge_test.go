package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"
)

func TestJudgeBandsFlag(t *testing.T) {
	if b, err := parseBands("0.2,0.8"); err != nil || b.Low != 0.2 || b.High != 0.8 {
		t.Fatalf("parseBands: %+v %v", b, err)
	}
	for _, bad := range []string{"0.8,0.2", "0.5", "x,y", "-0.1,0.5", "0.5,1.2"} {
		if _, err := parseBands(bad); err == nil {
			t.Errorf("--bands %q was accepted", bad)
		}
	}
}

// Items are read in order, a missing id is numbered by line, and a line with no
// state is refused by line number rather than judged as "null".
func TestJudgeItems(t *testing.T) {
	p := filepath.Join(t.TempDir(), "items.jsonl")
	_ = os.WriteFile(p, []byte("{\"id\":\"a\",\"state\":\"one\"}\n\n{\"state\":{\"k\":2}}\n"), 0o644)
	items, err := readItems(p, "")
	if err != nil || len(items) != 2 || items[0].ID != "a" || items[1].ID != "3" {
		t.Fatalf("readItems: %+v %v", items, err)
	}
	_ = os.WriteFile(p, []byte("{\"id\":\"a\"}\n"), 0o644)
	if _, err := readItems(p, ""); err == nil {
		t.Fatal("an item with no state was accepted")
	}
}

// The key is named, never read into output: a missing one is refused by NAME
// before any network call.
func TestJudgeNamesAMissingKey(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	_, _, err := judgeClassifier(nil)
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("want a refusal naming OPENROUTER_API_KEY, got %v", err)
	}
}

// A bare URL is a classifier: the flags reach the wire exactly as toolnexus
// ClassifierOptions would send them — model in the body, a ${VAR} header
// expanded at call time, an extra body field typed.
func TestJudgeAtAURLCarriesEveryOption(t *testing.T) {
	var got struct {
		model, auth string
		temp        any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got.model, _ = body["model"].(string)
		got.temp = body["seed"]
		got.auth = r.Header.Get("X-Judge-Auth")
		http.Error(w, "recorded", http.StatusTeapot)
	}))
	defer srv.Close()
	t.Setenv("JUDGE_TEST_TOKEN", "NOT_A_REAL_TOKEN")

	c, label, err := judgeClassifier([]string{"--url", srv.URL, "--model", "jev-selfhosted",
		"--header", "X-Judge-Auth=Bearer ${JUDGE_TEST_TOKEN}", "--param", "seed=7", "--retries", "0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(label, "jev-selfhosted") {
		t.Errorf("label %q does not name the model", label)
	}
	_, _ = c.Evaluate(context.Background(), "s", map[string]tn.Question{"q": tn.NoulQuestion{Instructions: "?"}})
	if got.model != "jev-selfhosted" || got.auth != "Bearer NOT_A_REAL_TOKEN" || got.temp != float64(7) {
		t.Fatalf("the endpoint saw model=%q auth-set=%v seed=%v", got.model, got.auth != "", got.temp)
	}

	// A key written straight into a credential header is refused before any call.
	if _, _, err := judgeClassifier([]string{"--url", srv.URL, "--header", "Authorization=Bearer abc123"}); err == nil {
		t.Fatal("a literal credential header was accepted")
	}
}
