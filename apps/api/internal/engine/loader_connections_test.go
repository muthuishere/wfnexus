package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

const wfTmpl = "name: %s\nsteps:\n  - id: a\n    prompt: p\n    output_schema:\n      type: object\n    mcp: [%s]\n"

func writeWF(t *testing.T, dir, name, mcp string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(sprintf(wfTmpl, name, mcp)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

// loaderFixture: proj-a (bound to acme, connections "crm" and "conn-only")
// and the default project, with an mcp.json naming "crm" and "opsrv".
func newLoaderFixture(t *testing.T) (*connFixture, string, string) {
	t.Helper()
	f := newConnFixture(t)
	root := t.TempDir()
	local, projA, work := filepath.Join(root, "local"), filepath.Join(root, "proj-a"), filepath.Join(root, "work")
	for _, d := range []string{local, projA, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal([]workflow.Source{{Name: "proj-a", Dir: projA}})
	if err := os.WriteFile(filepath.Join(work, "sources.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"crm":   map[string]any{"type": "remote", "url": "https://op.example.test/a"},
		"opsrv": map[string]any{"type": "remote", "url": "https://op.example.test/b"},
	}})
	if err := os.WriteFile(f.e.cfg.McpConfig, mcp, 0o600); err != nil {
		t.Fatal(err)
	}
	f.e.cfg.WorkDir, f.e.cfg.WorkflowsDir = work, local
	if err := f.st.CreateConnection(context.Background(), &store.Connection{TenantID: "acme", Name: "conn-only", Endpoint: "https://c.example.test", Tools: []string{"x"}}, nil); err != nil {
		t.Fatal(err)
	}
	writeWF(t, projA, "uses-conn", "crm")
	writeWF(t, projA, "uses-global", "opsrv")
	writeWF(t, local, "default-global", "opsrv")
	writeWF(t, local, "default-conn", "conn-only")
	return f, local, projA
}

func loaded(f *connFixture) map[string]bool {
	out := map[string]bool{}
	for k, d := range f.e.Definitions() {
		out[k] = true
		out[d.Name] = true
	}
	return out
}

// Switched on: a tenant project's mcp names are its connections, never the
// server's registry; the default project keeps the registry.
func TestTheLoaderResolvesATenantProjectsMCPToItsConnections(t *testing.T) {
	f, _, _ := newLoaderFixture(t)
	if err := f.e.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	got := loaded(f)
	if !got["uses-conn"] {
		t.Error("a tenant workflow naming its connection did not load")
	}
	if got["uses-global"] {
		t.Error("a tenant workflow naming only a server mcp server loaded")
	}
	if !got["default-global"] {
		t.Error("a default-project workflow naming a server mcp server did not load")
	}
	if got["default-conn"] {
		t.Error("a default-project workflow loaded with a tenant's connection name")
	}
	// The same rules on save.
	def := func(name, mcp string) *workflow.Definition {
		d, err := workflow.DecodeDefinition([]byte(sprintf(`{"name":%q,"steps":[{"id":"a","prompt":"p","output_schema":{"type":"object"},"mcp":[%q]}]}`, name, mcp)))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if _, err := f.e.SaveWorkflowIn("proj-a", def("save-conn", "crm")); err != nil {
		t.Errorf("save naming its connection: %v", err)
	}
	if _, err := f.e.SaveWorkflowIn("proj-a", def("save-global", "opsrv")); err == nil {
		t.Error("save into a tenant project naming a server mcp server was accepted")
	}
}

// Switched off: a tenant project's mcp names are refused at load and save.
func TestTheLoaderRefusesATenantProjectsMCPWhileOff(t *testing.T) {
	f, _, _ := newLoaderFixture(t)
	tenantConnectionsForTest = false
	if err := f.e.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	got := loaded(f)
	if got["uses-conn"] || got["uses-global"] {
		t.Error("a tenant project's mcp workflow loaded while connections are off")
	}
	if !got["default-global"] {
		t.Error("the default project changed while off")
	}
	d, _ := workflow.DecodeDefinition([]byte(`{"name":"save-off","steps":[{"id":"a","prompt":"p","output_schema":{"type":"object"},"mcp":["crm"]}]}`))
	if _, err := f.e.SaveWorkflowIn("proj-a", d); err == nil {
		t.Error("save naming a connection was accepted while off")
	}
}

// A project whose tenant cannot be read resolves no mcp name at all, rather
// than falling back to the server's registry.
func TestTheLoaderFailsClosedWhenATenantCannotBeRead(t *testing.T) {
	f, _, _ := newLoaderFixture(t)
	if _, err := f.st.DB().Exec(`DROP TABLE tenant_projects`); err != nil {
		t.Fatal(err)
	}
	if err := f.e.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	got := loaded(f)
	if got["uses-global"] || got["default-global"] {
		t.Errorf("an mcp workflow loaded while project tenants could not be read: %v", got)
	}
}
