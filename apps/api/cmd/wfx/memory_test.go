package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// `wfx memory write|rm|dream` reach the memory-home API, and an incomplete
// command is refused before the server is asked.
func TestMemoryCommandsCallTheAPI(t *testing.T) {
	tempContexts(t)
	var hit string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.Method + " " + r.URL.RequestURI()
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("WFX_API", srv.URL)
	t.Setenv("WFX_API_TOKEN", "")

	if err := memoryCmd([]string{"write", "triage", "note:deploy", "check", "staging", "--project", "demo"}); err != nil {
		t.Fatal(err)
	}
	if hit != "PUT /api/workflows/triage/memory/file?project=demo&target=note%3Adeploy" || body["body"] != "check staging" {
		t.Fatalf("write hit %s with %v", hit, body)
	}
	if err := memoryCmd([]string{"rm", "triage", "note:deploy"}); err != nil || hit != "DELETE /api/workflows/triage/memory/file?target=note%3Adeploy" {
		t.Fatalf("rm hit %s (%v)", hit, err)
	}
	if err := memoryCmd([]string{"dream", "triage"}); err != nil || hit != "POST /api/workflows/triage/memory/dream?" {
		t.Fatalf("dream hit %s (%v)", hit, err)
	}
	hit = ""
	for _, args := range [][]string{{"write", "triage", "note:x"}, {"read", "triage"}, {"show"}} {
		if err := memoryCmd(args); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
	if hit != "" {
		t.Fatalf("an incomplete command reached the server: %s", hit)
	}
}
