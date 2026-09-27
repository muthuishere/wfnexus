package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

const proposalWorkflow = `name: hello
description: say hello
steps:
  - id: greet
    name: Greet
    prompt: "say hello"
    tools: [read]
    output_schema:
      type: object
      properties:
        ok: {type: boolean}
`

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type proposalFixture struct {
	h    http.Handler
	st   *store.Store
	eng  *engine.Engine
	repo string
}

// proposalServer is a loopback server with one project, "demo", whose
// .wfx/workflows is a git repository with no remote (solo mode). No
// classifier is configured, so every review is "unreviewed".
func proposalServer(t *testing.T, gitBacked bool) proposalFixture {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "")
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	repo := t.TempDir()
	wf := filepath.Join(repo, ".wfx", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "hello.yaml"), []byte(proposalWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	if gitBacked {
		runGit(t, repo, "init", "-q", "-b", "main")
		runGit(t, repo, "config", "commit.gpgsign", "false")
		runGit(t, repo, "add", "-A")
		runGit(t, repo, "commit", "-q", "-m", "init")
	}
	cfg := config.Config{Addr: "127.0.0.1:0", WorkDir: t.TempDir(), WorkflowsDir: t.TempDir()}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if _, err := eng.ImportRepo(context.Background(), "demo", repo, ""); err != nil {
		t.Fatal(err)
	}
	return proposalFixture{h: New(eng, st, nil, cfg.Addr, "", nil), st: st, eng: eng, repo: repo}
}

func (f proposalFixture) do(t *testing.T, method, path string, body any, actor string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if actor != "" {
		req.Header.Set("X-WFX-Actor", actor)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f proposalFixture) editedHello(t *testing.T, desc string) map[string]any {
	t.Helper()
	def := *f.eng.Definitions()["demo/hello"]
	def.Description = desc
	return map[string]any{"definition": def}
}

func (f proposalFixture) checkoutHello(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.repo, ".wfx", "workflows", "hello.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSavingAGitBackedWorkflowOpensAProposalInsteadOfWriting(t *testing.T) {
	f := proposalServer(t, true)
	code, out := f.do(t, "PUT", "/api/workflows/hello?project=demo", f.editedHello(t, "proposed"), "ana")
	if code != 202 {
		t.Fatalf("save = %d %v", code, out)
	}
	p := out["proposal"].(map[string]any)
	if p["kind"] != "edit" || p["status"] != "pending" || p["reviewVerdict"] != "unreviewed" || p["createdBy"] != "ana" {
		t.Fatalf("proposal = %v", p)
	}
	if !strings.HasPrefix(p["branch"].(string), "wfx/edit-hello-") {
		t.Fatalf("branch = %v", p["branch"])
	}
	if strings.Contains(f.checkoutHello(t), "proposed") {
		t.Fatal("the tracked checkout was written before approval")
	}

	code, got := f.do(t, "GET", "/api/proposals/"+p["id"].(string), nil, "")
	if code != 200 || !strings.Contains(got["diff"].(string), "proposed") {
		t.Fatalf("get = %d %v", code, got)
	}
	code, _ = f.do(t, "GET", "/api/proposals?project=demo&workflow=hello", nil, "")
	if code != 200 {
		t.Fatalf("list = %d", code)
	}
}

func TestApprovingAProposalNeedsAnActorThenMergesAndReloads(t *testing.T) {
	f := proposalServer(t, true)
	_, out := f.do(t, "PUT", "/api/workflows/hello?project=demo", f.editedHello(t, "approved edit"), "ana")
	id := out["proposal"].(map[string]any)["id"].(string)

	if code, _ := f.do(t, "POST", "/api/proposals/"+id+"/approve", map[string]any{}, ""); code != 400 {
		t.Fatalf("an approve with no actor = %d, want 400", code)
	}
	code, got := f.do(t, "POST", "/api/proposals/"+id+"/approve", map[string]any{"actor": "bo"}, "")
	if code != 200 {
		t.Fatalf("approve = %d %v", code, got)
	}
	p := got["proposal"].(map[string]any)
	if p["status"] != "merged" || p["decidedBy"] != "bo" {
		t.Fatalf("approved proposal = %v", p)
	}
	if !strings.Contains(f.checkoutHello(t), "approved edit") {
		t.Fatal("the checkout did not get the approved change")
	}
	if d := f.eng.Definitions()["demo/hello"]; d == nil || d.Description != "approved edit" {
		t.Fatal("the catalog was not reloaded after the merge")
	}
	if code, _ := f.do(t, "POST", "/api/proposals/"+id+"/reject", map[string]any{"actor": "bo"}, ""); code != 409 {
		t.Fatalf("rejecting a merged proposal = %d, want 409", code)
	}
}

func TestRejectingAProposalRecordsTheReasonAndLeavesTheCheckout(t *testing.T) {
	f := proposalServer(t, true)
	code, out := f.do(t, "DELETE", "/api/workflows/demo%2Fhello", nil, "ana")
	if code != 202 {
		t.Fatalf("delete = %d %v", code, out)
	}
	p := out["proposal"].(map[string]any)
	if p["kind"] != "delete" {
		t.Fatalf("proposal = %v", p)
	}
	code, got := f.do(t, "POST", "/api/proposals/"+p["id"].(string)+"/reject", map[string]any{"actor": "bo", "reason": "still used"}, "")
	if code != 200 {
		t.Fatalf("reject = %d %v", code, got)
	}
	r := got["proposal"].(map[string]any)
	if r["status"] != "rejected" || r["reason"] != "still used" {
		t.Fatalf("rejected = %v", r)
	}
	if f.checkoutHello(t) == "" || f.eng.Definitions()["demo/hello"] == nil {
		t.Fatal("a rejected delete removed the workflow")
	}
	if b := runGit(t, f.repo, "branch", "--list", "wfx/*"); b != "" {
		t.Fatalf("the rejected branch survived: %s", b)
	}
}

func TestAnInvalidDefinitionIsRefusedBeforeAnyBranchIsCut(t *testing.T) {
	f := proposalServer(t, true)
	def := *f.eng.Definitions()["demo/hello"]
	def.Steps = append([]workflow.Step{}, def.Steps...)
	def.Steps[0].OutputSchema = nil // "needs output_schema"
	code, _ := f.do(t, "PUT", "/api/workflows/hello?project=demo", map[string]any{"definition": def}, "ana")
	if code != 400 {
		t.Fatalf("an invalid save = %d, want 400", code)
	}
	if b := runGit(t, f.repo, "branch", "--list", "wfx/*"); b != "" {
		t.Fatalf("an invalid save cut a branch: %s", b)
	}
}

func TestAPlainDirectoryProjectStillSavesDirectly(t *testing.T) {
	f := proposalServer(t, false)
	code, out := f.do(t, "PUT", "/api/workflows/hello?project=demo", f.editedHello(t, "direct"), "ana")
	if code != 200 || out["proposal"] != nil {
		t.Fatalf("save = %d %v", code, out)
	}
	if !strings.Contains(f.checkoutHello(t), "direct") {
		t.Fatal("a non-git project was not written directly")
	}
}

func TestDriftListsAnUncommittedWorkflowAndTurnsItIntoAProposal(t *testing.T) {
	f := proposalServer(t, true)
	fresh := strings.Replace(proposalWorkflow, "name: hello", "name: fresh", 1)
	dir := filepath.Join(f.repo, ".wfx", "workflows", "fresh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(fresh), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/proposals/drift?project=demo", nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var drift []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &drift)
	if rec.Code != 200 || len(drift) != 1 || drift[0]["workflow"] != "fresh" || drift[0]["kind"] != "create" {
		t.Fatalf("drift = %d %s", rec.Code, rec.Body.String())
	}
	code, out := f.do(t, "POST", "/api/proposals/drift", map[string]any{"project": "demo", "workflow": "fresh"}, "ana")
	if code != 202 || out["validation"] != "ok" {
		t.Fatalf("drift proposal = %d %v", code, out)
	}
	id := out["proposal"].(map[string]any)["id"].(string)
	if code, got := f.do(t, "POST", "/api/proposals/"+id+"/approve", nil, "bo"); code != 200 {
		t.Fatalf("approve = %d %v", code, got)
	}
	if st := runGit(t, f.repo, "status", "--porcelain"); st != "" {
		t.Fatalf("after approving the drift the checkout is still dirty:\n%s", st)
	}
}

func TestDecidedProposalShowsDiffAfterBranchDeletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		status string
	}{
		{name: "merged", action: "approve", status: "merged"},
		{name: "rejected", action: "reject", status: "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := proposalServer(t, true)
			marker := "visible in decided proposal"
			code, out := f.do(t, "PUT", "/api/workflows/hello?project=demo", f.editedHello(t, marker), "ana")
			if code != 202 {
				t.Fatalf("save = %d %v", code, out)
			}
			proposal := out["proposal"].(map[string]any)
			id := proposal["id"].(string)
			body := map[string]any{"actor": "bo"}
			code, decided := f.do(t, "POST", "/api/proposals/"+id+"/"+tc.action, body, "")
			if code != 200 {
				t.Fatalf("decide = %d %v", code, decided)
			}
			if branch := runGit(t, f.repo, "branch", "--list", "wfx/*"); branch != "" {
				t.Fatalf("proposal branch survived: %s", branch)
			}
			code, got := f.do(t, "GET", "/api/proposals/"+id, nil, "")
			if code != 200 {
				t.Fatalf("get decided proposal = %d %v", code, got)
			}
			gotProposal := got["proposal"].(map[string]any)
			if gotProposal["status"] != tc.status || gotProposal["commit"] == "" {
				t.Fatalf("decided proposal = %v", gotProposal)
			}
			if diff := got["diff"].(string); !strings.Contains(diff, marker) {
				t.Fatalf("decided proposal diff does not contain %q: %q", marker, diff)
			}
		})
	}
}
