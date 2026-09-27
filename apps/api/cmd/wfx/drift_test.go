package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// driftServer answers the drift endpoints the way the real server does and
// records what the CLI sent.
func driftServer(t *testing.T, rows []driftRow) (*[]string, *map[string]any) {
	t.Helper()
	var seen []string
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/proposals/drift":
			_ = json.NewEncoder(w).Encode(rows)
		case r.Method == "POST" && r.URL.Path == "/api/proposals/drift":
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "validation": "ok",
				"proposal": map[string]any{"id": "p1", "workflow": posted["workflow"], "kind": "edit", "branch": "wfx/p1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WFX_API", srv.URL)
	return &seen, &posted
}

func TestDriftListsWithTheProjectFilter(t *testing.T) {
	seen, _ := driftServer(t, []driftRow{{Project: "acme", Workflow: "hello", Kind: "edit", Files: []string{"workflow.yaml"}}})
	if err := workflowCmd([]string{"drift", "--project", "acme"}); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0] != "GET /api/proposals/drift?project=acme" {
		t.Fatalf("requests = %v", *seen)
	}
}

func TestDriftProposeInfersAnUnambiguousProject(t *testing.T) {
	seen, posted := driftServer(t, []driftRow{{Project: "acme", Workflow: "hello", Kind: "edit"}})
	if err := workflowCmd([]string{"drift", "--propose", "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || (*seen)[1] != "POST /api/proposals/drift" {
		t.Fatalf("requests = %v", *seen)
	}
	if (*posted)["project"] != "acme" || (*posted)["workflow"] != "hello" {
		t.Fatalf("posted %v", *posted)
	}
	if _, ok := (*posted)["actor"]; !ok {
		t.Fatalf("posted no actor claim: %v", *posted)
	}
}

func TestDriftProposeRefusesToGuessBetweenProjects(t *testing.T) {
	seen, _ := driftServer(t, []driftRow{{Project: "a", Workflow: "hello"}, {Project: "b", Workflow: "hello"}})
	err := workflowCmd([]string{"drift", "--propose", "hello"})
	if err == nil || !strings.Contains(err.Error(), "--project") {
		t.Fatalf("err = %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("it posted anyway: %v", *seen)
	}
	if err := workflowCmd([]string{"drift", "--propose", "nothere"}); err == nil {
		t.Fatal("a workflow with no drift was proposed")
	}
}
