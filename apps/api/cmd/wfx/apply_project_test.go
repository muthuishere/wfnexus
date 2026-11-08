package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// "Is it scheduled on the server" is answered by the server's copy, with the
// next fire time, so a Friday schedule reads as a Friday.
func TestScheduleLinesNameTheNextFire(t *testing.T) {
	// Monday 2026-09-28 10:00 UTC; "0 9 * * 5" is Friday 09:00.
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	got := scheduleLines([]workflow.Schedule{{Cron: "0 9 * * 5"}, {Cron: "not a cron"}}, now)
	if len(got) != 2 || !strings.Contains(got[0], "next Fri 2026-10-02 09:00 UTC") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got[1], "invalid") {
		t.Fatalf("an invalid cron was not called out: %q", got[1])
	}
}

// captureStdout runs f and returns what it printed.
func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := f()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), err
}

// `wfx apply --project p` saves into that project, and a git-backed project's
// answer, a proposal, is reported as a proposal with the command that approves
// it. It used to print "installed" with an empty path for a change that was
// still waiting on review.
func TestApplyIntoAGitProjectReportsTheProposal(t *testing.T) {
	tempContexts(t)
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"ok":true,"proposal":{"id":"p-1","project":"team","kind":"create","branch":"wfx/create-w","status":"pending","reviewVerdict":"approve","reviewScore":0.91}}`)
	}))
	defer srv.Close()
	t.Setenv("WFX_API", srv.URL)

	wf := workflowFile(t, t.TempDir(), plainWorkflow)
	out, err := captureStdout(t, func() error { return apply([]string{wf, "--project", "team"}, true) })
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/workflows/w?project=team" {
		t.Fatalf("saved to %q, not into the project", gotPath)
	}
	for _, want := range []string{"proposed create w in team", "p-1", "wfx workflow approve p-1", "approve (0.91)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "installed") {
		t.Fatalf("a proposal was reported as installed:\n%s", out)
	}
}

// `wfx apply <dir>/workflow.yaml` sends the files beside it: applying a
// folder-form workflow used to save it without its scripts.
func TestApplyAFolderFormWorkflowSendsItsFiles(t *testing.T) {
	tempContexts(t)
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"ok":true,"path":"/x/w/workflow.yaml"}`)
	}))
	defer srv.Close()
	t.Setenv("WFX_API", srv.URL)
	dir := filepath.Join(t.TempDir(), "w")
	_ = os.MkdirAll(filepath.Join(dir, "lib"), 0o755)
	for p, b := range map[string]string{workflow.DefinitionFile: plainWorkflow, "collect.py": "print(1)\n", "lib/x.py": "y = 2\n"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := captureStdout(t, func() error { return apply([]string{filepath.Join(dir, workflow.DefinitionFile)}, true) }); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Definition struct {
			Files []workflow.File `json:"files"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Definition.Files) != 2 || got.Definition.Files[0].Path != "collect.py" || string(got.Definition.Files[1].Body) != "y = 2\n" {
		t.Fatalf("files sent: %+v", got.Definition.Files)
	}
}
