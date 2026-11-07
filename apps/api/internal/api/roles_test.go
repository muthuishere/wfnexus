package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// ADR 0028: publisher -> workflow_admin, viewer -> workflow_runner, on every
// workflow of the user's project (all of them when unscoped), at boot.
func TestLegacyRolesConvertAtBoot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	defs := map[string]*workflow.Definition{"w1": {Name: "w1"}, "s/w2": {Name: "w2", Source: "s"}}
	eng := engine.New(config.Config{Addr: remoteAddr}, st, nil, defs, skills.Load(), nil)
	for name, role := range map[string]string{"pub": "legacy:publisher", "view": "legacy:viewer"} {
		u := &store.User{Name: name, Role: role}
		if name == "pub" {
			u.Project = "s"
		}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	ConvertLegacyRoles(ctx, eng, st)

	grants := func(name string) map[string]string {
		u, _ := st.UserByName(ctx, name)
		if u.Role != "" {
			t.Fatalf("%s kept role %q", name, u.Role)
		}
		gs, _ := st.GrantsOf(ctx, u.ID)
		out := map[string]string{}
		for _, g := range gs {
			out[g.Name] = g.Role
		}
		return out
	}
	if g := grants("pub"); len(g) != 1 || g["s/w2"] != store.WorkflowAdmin {
		t.Fatalf("publisher scoped to s: %v", g)
	}
	if g := grants("view"); len(g) != 2 || g["w1"] != store.WorkflowRunner || g["s/w2"] != store.WorkflowRunner {
		t.Fatalf("unscoped viewer: %v", g)
	}
}
