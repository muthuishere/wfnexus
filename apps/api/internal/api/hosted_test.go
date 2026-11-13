package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// hostedFixture is a hosted server (config.Hosted) on SQLite.
type hostedFixture struct {
	h   http.Handler
	st  *store.Store
	eng *engine.Engine
	cfg config.Config
}

// hostedServer builds a hosted server. addr decides whether a subject is
// required: loopback answers with no subject, anything else demands one.
func hostedServer(t *testing.T, addr string) hostedFixture {
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
	cfg := config.Config{Addr: addr, Hosted: true, WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(), MaxConcurrentRuns: 4,
		SecretKeyEnv: "WFX_HOSTED_TEST_NO_SUCH_KEY", SecretKeyPath: filepath.Join(t.TempDir(), "secret.key")}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	return hostedFixture{h: New(eng, st, nil, cfg.Addr, "", nil), st: st, eng: eng, cfg: cfg}
}

func (f hostedFixture) do(t *testing.T, method, path string, body any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// addWorkflow writes a workflow file into a created project and reloads.
func (f hostedFixture) addWorkflow(t *testing.T, project, file, body string) {
	t.Helper()
	dir := filepath.Join(f.cfg.WorkDir, "projects", project, ".wfx", "workflows")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
}

func (f hostedFixture) waitRun(t *testing.T, id string) *store.Run {
	t.Helper()
	rid := uuid.MustParse(id)
	deadline := time.Now().Add(20 * time.Second)
	for {
		run, err := f.st.GetRun(context.Background(), rid)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == "done" || run.Status == "failed" || run.Status == "cancelled" || run.Status == "awaiting_approval" {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s still %s", id, run.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func sameNamed(project string) string {
	return fmt.Sprintf(`name: deploy
concurrency: 1
steps:
  - id: say
    run: sleep 0.3; echo "deployed %s"
`, project)
}

// W1 done-when: two projects with a workflow of the same name run side by
// side — each reached as <project>/<name>, each run filed under its own
// project, neither's `concurrency: 1` holding the other.
func TestHostedTwoProjectsSameWorkflowNameRunSideBySide(t *testing.T) {
	f := hostedServer(t, "127.0.0.1:0")
	for _, p := range []string{"acme", "globex"} {
		if code, out := f.do(t, "POST", "/api/projects", map[string]any{"name": p, "create": true}, nil); code != 201 {
			t.Fatalf("create %s = %d %v", p, code, out)
		}
		f.addWorkflow(t, p, "deploy.yaml", sameNamed(p))
	}
	// Both are in the registry.
	rows, err := f.st.ListProjectRows(context.Background())
	if err != nil || len(rows) != 2 || rows[0].Name != "acme" || rows[1].Name != "globex" {
		t.Fatalf("projects registry = %+v, %v", rows, err)
	}
	// Hosted: no bare-name alias — `deploy` alone names nothing.
	if f.eng.Definitions()["deploy"] != nil {
		t.Fatal("hosted server must not alias a project workflow by its bare name")
	}
	if code, _ := f.do(t, "POST", "/api/workflows/deploy/runs", map[string]any{}, nil); code != 404 {
		t.Fatalf("bare name run = %d, want 404", code)
	}

	ids := map[string]string{}
	for _, p := range []string{"acme", "globex"} {
		code, out := f.do(t, "POST", "/api/workflows/"+p+"%2Fdeploy/runs", map[string]any{}, nil)
		if code != 201 {
			t.Fatalf("run %s/deploy = %d %v", p, code, out)
		}
		ids[p] = out["run"].(map[string]any)["id"].(string)
	}
	for p, id := range ids {
		run := f.waitRun(t, id)
		if run.Status != "done" || run.Project != p || run.Workflow != p+"/deploy" {
			t.Fatalf("%s run = status %s project %s workflow %s (%s)", p, run.Status, run.Project, run.Workflow, run.Error)
		}
		steps, _ := f.st.ListSteps(context.Background(), run.ID)
		if len(steps) != 1 || !strings.Contains(string(steps[0].Output)+steps[0].RawText, "deployed "+p) {
			t.Fatalf("%s ran the wrong project's workflow: %+v", p, steps)
		}
	}

	// Deleting a project removes its registry row.
	if code, out := f.do(t, "DELETE", "/api/projects/globex", nil, nil); code != 200 {
		t.Fatalf("delete = %d %v", code, out)
	}
	if ok, _ := f.st.ProjectRegistered(context.Background(), "globex"); ok {
		t.Fatal("deleted project still registered")
	}
}

func TestHostedRefusesServerFolders(t *testing.T) {
	f := hostedServer(t, "127.0.0.1:0")
	dir := t.TempDir()
	if code, _ := f.do(t, "POST", "/api/projects", map[string]any{"name": "x", "create": true, "repo": dir}, nil); code != 400 {
		t.Fatalf("create with a server folder = %d, want 400", code)
	}
	if code, _ := f.do(t, "POST", "/api/projects", map[string]any{"name": "y", "repo": dir}, nil); code != 400 {
		t.Fatalf("import of a server folder = %d, want 400", code)
	}
}
