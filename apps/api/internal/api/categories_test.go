package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A project's category is what lets it be offered the right templates first,
// so it has to survive creation, a change, and a reload — and a bad one must
// not leave a half-created project behind.
func TestProjectCategoryLifecycle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wfnexus.db")
	if err := store.Migrate("sqlite", dbPath); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	local := filepath.Join(dir, "workflows")
	_ = os.MkdirAll(local, 0o755)
	cfg := config.Config{Addr: "127.0.0.1:8090", WorkDir: filepath.Join(dir, "work"), WorkflowsDir: local}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	h := New(eng, st, nil, cfg.Addr, "", nil)

	if res := do(h, "GET", "/api/categories", "", nil); res.Code != 200 {
		t.Fatalf("categories: %d %s", res.Code, res.Body)
	} else {
		var cats []workflow.Category
		_ = json.Unmarshal(res.Body.Bytes(), &cats)
		if len(cats) != len(workflow.Categories) || cats[0].ID != "development" {
			t.Fatalf("categories: %+v", cats)
		}
	}

	// A bad category is refused BEFORE anything is created.
	if res := do(h, "POST", "/api/projects", "", map[string]any{"create": true, "name": "books", "category": "accountancy"}); res.Code != 400 {
		t.Fatalf("bad category: %d %s", res.Code, res.Body)
	}
	if res := do(h, "GET", "/api/projects/books", "", nil); res.Code != 404 {
		t.Fatalf("a refused create left a project behind: %d", res.Code)
	}

	res := do(h, "POST", "/api/projects", "", map[string]any{"create": true, "name": "books", "category": "finance"})
	if res.Code != 201 {
		t.Fatalf("create: %d %s", res.Code, res.Body)
	}
	var p engine.Project
	_ = json.Unmarshal(res.Body.Bytes(), &p)
	if p.Category != "finance" {
		t.Fatalf("created without its category: %+v", p)
	}

	res = do(h, "PATCH", "/api/projects/books", "", map[string]any{"category": "legal"})
	_ = json.Unmarshal(res.Body.Bytes(), &p)
	if res.Code != 200 || p.Category != "legal" {
		t.Fatalf("patch: %d %s", res.Code, res.Body)
	}
	// local is a project too, with no import behind it.
	if res := do(h, "PATCH", "/api/projects/local", "", map[string]any{"category": "development"}); res.Code != 200 {
		t.Fatalf("patch local: %d %s", res.Code, res.Body)
	}
	if res := do(h, "PATCH", "/api/projects/nobody", "", map[string]any{"category": "hr"}); res.Code != 400 {
		t.Fatalf("patch of an unknown project: %d", res.Code)
	}
	// Clearing it is allowed.
	res = do(h, "PATCH", "/api/projects/books", "", map[string]any{"category": ""})
	var cleared engine.Project // fresh: an empty category is omitted, so decoding into p would keep "legal"
	_ = json.Unmarshal(res.Body.Bytes(), &cleared)
	if res.Code != 200 || cleared.Category != "" {
		t.Fatalf("clear: %d %+v", res.Code, cleared)
	}
}
