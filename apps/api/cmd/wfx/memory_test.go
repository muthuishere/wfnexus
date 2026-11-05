package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// `wfx memory add` and `wfx memory edit` reach the memory API with the text in
// the body, and refuse an incomplete command before the server is asked.
func TestMemoryAddAndEditCallTheAPI(t *testing.T) {
	tempContexts(t)
	var hit string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.Method + " " + r.URL.RequestURI()
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"m-1","kind":"note","body":"x"}`))
	}))
	defer srv.Close()
	t.Setenv("WFX_API", srv.URL)
	t.Setenv("WFX_API_TOKEN", "")

	if err := memoryCmd([]string{"add", "--workflow", "triage", "--project", "demo", "check", "staging", "first"}); err != nil {
		t.Fatal(err)
	}
	if hit != "POST /api/workflows/triage/memory?project=demo" || body["body"] != "check staging first" {
		t.Fatalf("add hit %s with %v", hit, body)
	}
	if err := memoryCmd([]string{"edit", "m-1", "check prod first"}); err != nil {
		t.Fatal(err)
	}
	if hit != "PATCH /api/memory/m-1" || body["body"] != "check prod first" {
		t.Fatalf("edit hit %s with %v", hit, body)
	}
	hit = ""
	for _, args := range [][]string{{"add", "text-without-workflow"}, {"add", "--workflow", "w"}, {"edit", "m-1"}, {"edit"}} {
		if err := memoryCmd(args); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
	if hit != "" {
		t.Fatalf("an incomplete command reached the server: %s", hit)
	}
}
