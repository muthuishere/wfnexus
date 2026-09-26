package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	_, _, err := judgeClassifier("", "")
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("want a refusal naming OPENROUTER_API_KEY, got %v", err)
	}
}
