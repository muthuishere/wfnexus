package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/secrets"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// fakeProduct is an MCP endpoint that answers only with the expected bearer
// and offers three tools. fail_item answers with the header it received, the
// way a careless server error might.
type fakeProduct struct {
	srv      *httptest.Server
	authed   atomic.Int32
	refused  atomic.Int32
	deleted  atomic.Int32
	expected string
}

func newFakeProduct(t *testing.T, credential string) *fakeProduct {
	t.Helper()
	f := &fakeProduct{expected: "Bearer " + credential}
	tk, err := tn.CreateToolkit(context.Background(), tn.Options{Builtins: map[string]bool{"none": true}})
	if err != nil {
		t.Fatal(err)
	}
	var lastAuth atomic.Value
	lastAuth.Store("")
	obj := tn.JSONSchema{"type": "object", "properties": map[string]any{}}
	tk.Register(
		tn.Tool{Name: "read_item", Description: "read", InputSchema: obj, Execute: func(map[string]any, *tn.ToolContext) (tn.ToolResult, error) {
			return tn.ToolResult{Output: "item-ok"}, nil
		}},
		tn.Tool{Name: "delete_item", Description: "delete", InputSchema: obj, Execute: func(map[string]any, *tn.ToolContext) (tn.ToolResult, error) {
			f.deleted.Add(1)
			return tn.ToolResult{Output: "deleted"}, nil
		}},
		tn.Tool{Name: "fail_item", Description: "fails", InputSchema: obj, Execute: func(map[string]any, *tn.ToolContext) (tn.ToolResult, error) {
			return tn.ToolResult{Output: "upstream refused request with " + lastAuth.Load().(string), IsError: true}, nil
		}},
	)
	h := tk.MCPHandler(tn.MCPHandlerConfig{Name: "fake"})
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != f.expected {
			f.refused.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.authed.Add(1)
		lastAuth.Store(r.Header.Get("Authorization"))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { f.srv.Close(); tk.Close() })
	return f
}

type connFixture struct {
	e                 *Engine
	st                *store.Store
	tenantRun, defRun uuid.UUID
	credential        string
	product           *fakeProduct
}

func newConnFixture(t *testing.T) *connFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WFX_TEST_CONNECTION_KEY", key)
	// A global mcp.json that ALSO names "crm", pointing elsewhere: a tenant
	// run must never use it.
	opTk, err := tn.CreateToolkit(ctx, tn.Options{Builtins: map[string]bool{"none": true}})
	if err != nil {
		t.Fatal(err)
	}
	opTk.Register(tn.Tool{Name: "operator_tool", Description: "operator only", InputSchema: tn.JSONSchema{"type": "object", "properties": map[string]any{}},
		Execute: func(map[string]any, *tn.ToolContext) (tn.ToolResult, error) {
			return tn.ToolResult{Output: "operator"}, nil
		}})
	other := httptest.NewServer(opTk.MCPHandler(tn.MCPHandlerConfig{Name: "operator"}))
	t.Cleanup(func() { other.Close(); opTk.Close() })
	mcpPath := filepath.Join(dir, "mcp.json")
	raw, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"crm": map[string]any{"type": "remote", "url": other.URL}}})
	if err := os.WriteFile(mcpPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{SecretKeyEnv: "WFX_TEST_CONNECTION_KEY", McpConfig: mcpPath}
	e := New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if e.Secrets() == nil {
		t.Fatal("no env store key in the fixture")
	}
	if _, err := st.CreateTenant(ctx, "acme", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.BindProject(ctx, "acme", "proj-a"); err != nil {
		t.Fatal(err)
	}
	tr, _ := st.CreateRun(ctx, "proj-a", "wf", json.RawMessage(`{}`))
	dr, _ := st.CreateRun(ctx, "local", "wf", json.RawMessage(`{}`))
	f := &connFixture{e: e, st: st, tenantRun: tr.ID, defRun: dr.ID, credential: "conn-cred-" + uuid.NewString()}
	f.product = newFakeProduct(t, f.credential)
	sealed, err := e.Secrets().Seal(f.credential)
	if err != nil {
		t.Fatal(err)
	}
	c := &store.Connection{TenantID: "acme", Name: "crm", Endpoint: f.product.srv.URL, HeaderScheme: "Bearer", Tools: []string{"read_item", "fail_item"}}
	if err := st.CreateConnection(ctx, c, sealed); err != nil {
		t.Fatal(err)
	}
	tenantConnectionsForTest = true
	t.Cleanup(func() { tenantConnectionsForTest = false })
	return f
}

func connToolNames(ts []tn.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func connTool(ts []tn.Tool, name string) (tn.Tool, bool) {
	for _, t := range ts {
		if t.Name == name {
			return t, true
		}
	}
	return tn.Tool{}, false
}

// A run in a tenant gets ITS connection, with only the allow-listed tools,
// authenticated with the sealed credential, and the credential is redacted.
func TestATenantRunGetsOnlyItsConnectionsAllowedTools(t *testing.T) {
	f := newConnFixture(t)
	ctx := withStepRun(context.Background(), f.tenantRun)
	global, tk, tools, err := f.e.splitStepMCP(ctx, "s", []string{"crm"})
	if err != nil {
		t.Fatal(err)
	}
	defer tk.Close()
	if global != nil {
		t.Fatalf("a tenant run resolves %v from mcp.json", global)
	}
	if got := strings.Join(connToolNames(tools), ","); got != "crm_fail_item,crm_read_item" {
		t.Fatalf("tools: %s", got)
	}
	read, _ := connTool(tools, "crm_read_item")
	res, err := read.Execute(map[string]any{}, nil)
	if err != nil || !strings.Contains(res.Output, "item-ok") {
		t.Fatalf("allowed tool: %+v %v", res, err)
	}
	if f.product.authed.Load() == 0 || f.product.refused.Load() != 0 {
		t.Fatalf("credential not presented: authed %d refused %d", f.product.authed.Load(), f.product.refused.Load())
	}
	if f.product.deleted.Load() != 0 {
		t.Fatal("a tool off the list ran")
	}
	// The credential an endpoint echoes back is scrubbed from what the run
	// records.
	fail, _ := connTool(tools, "crm_fail_item")
	res, _ = fail.Execute(map[string]any{}, nil)
	if !strings.Contains(res.Output, f.credential) {
		t.Fatalf("fixture: the endpoint did not echo the credential: %q", res.Output)
	}
	if out := redactText(res.Output, f.e.redactions.get(f.tenantRun)); strings.Contains(out, f.credential) {
		t.Fatalf("the credential survives redaction: %q", out)
	}
	ev, _ := json.Marshal(redactEvent(map[string]any{"output": res.Output}, f.e.redactions.get(f.tenantRun)))
	if strings.Contains(string(ev), f.credential) {
		t.Fatalf("the credential survives event redaction: %s", ev)
	}
}

// Narrowing the list applies to the next step; an empty list gives no tools
// (toolnexus would read an empty list as "all").
func TestTheAllowListNarrowsAndAnEmptyListGivesNothing(t *testing.T) {
	f := newConnFixture(t)
	ctx := withStepRun(context.Background(), f.tenantRun)
	if err := f.st.SetConnectionTools(context.Background(), "acme", "crm", []string{"read_item"}); err != nil {
		t.Fatal(err)
	}
	_, tk, tools, err := f.e.splitStepMCP(ctx, "s", []string{"crm"})
	if err != nil {
		t.Fatal(err)
	}
	if tk != nil {
		defer tk.Close()
	}
	if got := strings.Join(connToolNames(tools), ","); got != "crm_read_item" {
		t.Fatalf("narrowed tools: %s", got)
	}
	if err := f.st.SetConnectionTools(context.Background(), "acme", "crm", nil); err != nil {
		t.Fatal(err)
	}
	_, tk2, tools, err := f.e.splitStepMCP(ctx, "s", []string{"crm"})
	if err != nil {
		t.Fatal(err)
	}
	if tk2 != nil {
		defer tk2.Close()
	}
	if len(tools) != 0 {
		t.Fatalf("an empty allow-list gave %v", connToolNames(tools))
	}
}

// A tenant run never falls back to mcp.json, and a stale tenant fails closed.
func TestATenantRunCannotReachTheServersOwnMCP(t *testing.T) {
	f := newConnFixture(t)
	ctx := withStepRun(context.Background(), f.tenantRun)
	if _, _, _, err := f.e.splitStepMCP(ctx, "s", []string{"github"}); err == nil {
		t.Fatal("a name with no tenant connection resolved")
	}
	// The default tenant keeps mcp.json, exactly as before.
	global, tk, _, err := f.e.splitStepMCP(withStepRun(context.Background(), f.defRun), "s", []string{"crm"})
	if err != nil || tk != nil || len(global) != 1 {
		t.Fatalf("default tenant: %v %v %v", global, tk, err)
	}
	// No run in context: refused, not defaulted.
	if _, _, _, err := f.e.splitStepMCP(context.Background(), "s", []string{"crm"}); err == nil {
		t.Fatal("resolved with no run to read a tenant from")
	}
	// A run whose stored tenant disagrees with its project's: refused.
	if _, err := f.st.DB().Exec(`UPDATE workflow_runs SET tenant_id='default' WHERE id=?`, f.tenantRun.String()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.e.splitStepMCP(ctx, "s", []string{"crm"}); err == nil {
		t.Fatal("a stale run tenant resolved")
	}
}

// Off unless switched on: then every run resolves from mcp.json as before.
func TestTenantConnectionsAreOffUnlessSwitchedOn(t *testing.T) {
	f := newConnFixture(t)
	tenantConnectionsForTest = false
	global, tk, tools, err := f.e.splitStepMCP(withStepRun(context.Background(), f.tenantRun), "s", []string{"crm"})
	if err != nil || tk != nil || len(tools) != 0 || len(global) != 1 {
		t.Fatalf("switched off: %v %v %v %v", global, tk, tools, err)
	}
	if f.product.authed.Load() != 0 {
		t.Fatal("the tenant's endpoint was called while switched off")
	}
}

func TestATenantRunsConnectionsNeverGoToAWorker(t *testing.T) {
	f := newConnFixture(t)
	if err := f.e.refuseTenantConnectionsOnWorker(context.Background(), f.tenantRun, "s", []string{"crm"}); err == nil {
		t.Fatal("a tenant run's connection step was placed on a worker")
	}
	if err := f.e.refuseTenantConnectionsOnWorker(context.Background(), f.defRun, "s", []string{"crm"}); err != nil {
		t.Fatalf("a default-tenant step was refused: %v", err)
	}
	step := &workflow.Step{ID: "s", MCP: []string{"crm"}}
	if _, err := f.e.packAgentJob(context.Background(), f.tenantRun, &workflow.Definition{Name: "wf"}, step, "p", ""); err == nil {
		t.Fatal("packAgentJob packed a tenant connection step")
	}
}

// The credential is ciphertext at rest, and without the key it is not used.
func TestTheCredentialIsSealedAndNeedsTheKey(t *testing.T) {
	f := newConnFixture(t)
	var enc []byte
	if err := f.st.DB().QueryRow(`SELECT credential_enc FROM tenant_connections WHERE name='crm'`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || strings.Contains(string(enc), f.credential) {
		t.Fatal("the credential is not sealed at rest")
	}
	f.e.secrets = nil
	if _, _, _, err := f.e.splitStepMCP(withStepRun(context.Background(), f.tenantRun), "s", []string{"crm"}); err == nil {
		t.Fatal("a sealed credential was used with no key")
	}
}

// The agent a step builds carries exactly the allow-listed tenant tools.
func TestTheStepsAgentCarriesOnlyTheAllowedTenantTools(t *testing.T) {
	f := newConnFixture(t)
	step := &workflow.Step{ID: "s", MCP: []string{"crm"}}
	ag, closer, err := f.e.buildAgent(withStepRun(context.Background(), f.tenantRun), step, t.TempDir(), nil, nil, &tn.Hooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	have := map[string]bool{}
	for _, tl := range ag.Spec.Tools {
		have[tl.Name] = true
	}
	if !have["crm_read_item"] || !have["crm_fail_item"] || have["crm_delete_item"] || have["crm_operator_tool"] {
		t.Fatalf("agent tools: %v", have)
	}
}

// The filter on its own: only allowed MCP tools pass, whatever else a
// toolkit lists.
func TestAllowedToolsKeepsOnlyAllowedMCPTools(t *testing.T) {
	all := []tn.Tool{
		{Name: "crm_read_item", Source: tn.SourceMCP},
		{Name: "crm_delete_item", Source: tn.SourceMCP},
		{Name: "crm_read_item_x", Source: tn.SourceMCP},
		{Name: "bash", Source: tn.SourceBuiltin},
		{Name: "crm_fail_item", Source: tn.SourceBuiltin},
	}
	got := allowedTools(all, map[string]bool{"crm_read_item": true, "crm_fail_item": true})
	if len(got) != 1 || got[0].Name != "crm_read_item" {
		t.Fatalf("kept %v", connToolNames(got))
	}
	if len(allowedTools(all, map[string]bool{})) != 0 {
		t.Fatal("an empty allow-list kept tools")
	}
}

// The second wall on its own: the tenant toolkit, BEFORE the filter, lists
// only the allowed tools, and a connection with no tools builds no toolkit.
func TestTheTenantToolkitItselfListsOnlyAllowedTools(t *testing.T) {
	f := newConnFixture(t)
	ctx := withStepRun(context.Background(), f.tenantRun)
	_, tk, _, err := f.e.splitStepMCP(ctx, "s", []string{"crm"})
	if err != nil {
		t.Fatal(err)
	}
	defer tk.Close()
	for _, tl := range tk.Tools() {
		if tl.Name == "crm_delete_item" {
			t.Fatal("the tenant toolkit lists a tool off the allow-list")
		}
	}
	if err := f.st.SetConnectionTools(context.Background(), "acme", "crm", nil); err != nil {
		t.Fatal(err)
	}
	_, tk2, _, err := f.e.splitStepMCP(ctx, "s", []string{"crm"})
	if err != nil {
		t.Fatal(err)
	}
	if tk2 != nil {
		tk2.Close()
		t.Fatal("a connection with no allowed tools was connected to")
	}
}

// The fixture's own mcp.json server works, so a test that it is NOT used is
// a real test: the default tenant does get it.
func TestTheDefaultTenantStillGetsTheServersOwnMCP(t *testing.T) {
	f := newConnFixture(t)
	step := &workflow.Step{ID: "s", MCP: []string{"crm"}}
	ag, closer, err := f.e.buildAgent(withStepRun(context.Background(), f.defRun), step, t.TempDir(), nil, nil, &tn.Hooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	for _, tl := range ag.Spec.Tools {
		if tl.Name == "crm_operator_tool" {
			return
		}
	}
	t.Fatal("the default tenant did not get mcp.json's server")
}
