package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Subscriptions over the API: add validates (active, or error with the
// reason), pause/resume move the status, delete removes it, and the routes
// carry the registry permissions.
func TestSubscriptionsCRUDOverTheAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	regs := filepath.Join(dir, "registries.json")
	if err := os.WriteFile(regs, []byte(`{"channels":{"wa":{"kind":"command","run":"true"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	wf := filepath.Join(repo, ".wfx", "workflows")
	_ = os.MkdirAll(wf, 0o755)
	if err := os.WriteFile(filepath.Join(wf, "hello.yaml"), []byte(proposalWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Addr: "127.0.0.1:0", WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(), RegistriesPath: regs}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if _, err := eng.ImportRepo(context.Background(), "demo", repo, ""); err != nil {
		t.Fatal(err)
	}
	f := proposalFixture{h: New(eng, st, nil, cfg.Addr, "", nil), st: st, eng: eng}

	code, got := f.do(t, "PUT", "/api/subscriptions/pp", map[string]any{
		"channel": "wa", "chat": "g@g.us", "workflow": "demo/hello", "allow": []string{"*"}, "interval": "30s", "status": "paused"}, "")
	if code != 200 || got["status"] != "active" || got["startedAt"] == nil {
		t.Fatalf("add = %d %v", code, got)
	}
	code, got = f.do(t, "PUT", "/api/subscriptions/broken", map[string]any{"channel": "wa", "chat": "x", "workflow": "nope"}, "")
	if code != 200 || got["status"] != "error" || got["error"] == "" {
		t.Fatalf("a missing workflow must save as error: %d %v", code, got)
	}
	if code, _ := f.do(t, "PUT", "/api/subscriptions/bad", map[string]any{"channel": "wa", "chat": "x", "workflow": "w", "interval": "5s"}, ""); code != 400 {
		t.Fatalf("a bad interval = %d, want 400", code)
	}
	if code, got = f.do(t, "POST", "/api/subscriptions/pp/pause", nil, ""); code != 200 || got["status"] != "paused" {
		t.Fatalf("pause = %d %v", code, got)
	}
	if code, got = f.do(t, "POST", "/api/subscriptions/pp/resume", nil, ""); code != 200 || got["status"] != "active" {
		t.Fatalf("resume = %d %v", code, got)
	}
	if code, got = f.do(t, "GET", "/api/subscriptions/pp", nil, ""); code != 200 || got["subscription"] == nil {
		t.Fatalf("get = %d %v", code, got)
	}
	if code, _ := f.do(t, "DELETE", "/api/subscriptions/pp", nil, ""); code != 200 {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/subscriptions/pp", nil, ""); code != 404 {
		t.Fatalf("deleted get = %d", code)
	}
	if got := PermissionFor(http.MethodPut, "/api/subscriptions/x"); got != PermRegistryWrite {
		t.Fatalf("PUT needs %q", got)
	}
	if got := PermissionFor(http.MethodGet, "/api/subscriptions"); got != PermRegistryRead {
		t.Fatalf("GET needs %q", got)
	}
}
