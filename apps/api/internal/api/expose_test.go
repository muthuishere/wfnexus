package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

const exposeEcho = `name: echo
description: echo a greeting
input_schema:
  type: object
  properties:
    who: {type: string}
steps:
  - id: say
    run: echo "hello {{ .Input.who }}"
`

const exposeOther = `name: other
steps:
  - id: say
    run: echo other
`

type exposeFixture struct {
	h   http.Handler
	st  *store.Store
	eng *engine.Engine
}

func exposeServer(t *testing.T) exposeFixture {
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
	for name, body := range map[string]string{"echo.yaml": exposeEcho, "other.yaml": exposeOther} {
		if err := os.WriteFile(filepath.Join(wf, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A server key, so hook signing secrets can be sealed (first-boot file).
	cfg := config.Config{Addr: "127.0.0.1:0", WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(),
		SecretKeyEnv: "WFX_EXPOSE_TEST_NO_SUCH_KEY", SecretKeyPath: filepath.Join(t.TempDir(), "secret.key")}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if _, err := eng.ImportRepo(context.Background(), "demo", repo, ""); err != nil {
		t.Fatal(err)
	}
	return exposeFixture{h: New(eng, st, nil, cfg.Addr, "", nil), st: st, eng: eng}
}

func (f exposeFixture) call(t *testing.T, method, path string, body []byte, hdr map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func (f exposeFixture) issue(t *testing.T, path string, rotate bool) string {
	t.Helper()
	tok, _ := f.issueFull(t, path, rotate)
	return tok
}

// issueFull also returns the issue response (signingSecret for a hook).
func (f exposeFixture) issueFull(t *testing.T, path string, rotate bool) (string, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"rotate": rotate})
	code, out, _ := f.call(t, "POST", path, b, nil)
	if code != http.StatusCreated {
		t.Fatalf("issue %s = %d %v", path, code, out)
	}
	tok, _ := out["token"].(string)
	if !strings.HasPrefix(tok, "wfxe_") {
		t.Fatalf("no token in %v", out)
	}
	return tok, out
}

func TestHookIsOffUntilExposedAndOnlyForItsWorkflow(t *testing.T) {
	f := exposeServer(t)
	hook := "/hooks/demo/echo"
	body := []byte(`{"who":"ana"}`)

	if code, _, _ := f.call(t, "POST", hook, body, map[string]string{"Authorization": "Bearer wfxe_nope"}); code != 401 {
		t.Fatalf("unexposed workflow = %d, want 401", code)
	}
	tok, issued := f.issueFull(t, "/api/workflows/demo%2Fecho/expose/hook", false)
	auth := map[string]string{"Authorization": "Bearer " + tok}
	signing, _ := issued["signingSecret"].(string)
	if !strings.HasPrefix(signing, "wfxs_") || signing == tok {
		t.Fatalf("want a separate signing secret, got %v", issued)
	}

	// Exposing again without rotate is refused; GET never shows the token.
	if code, _, _ := f.call(t, "POST", "/api/workflows/demo%2Fecho/expose/hook", nil, nil); code != http.StatusConflict {
		t.Fatalf("second expose = %d, want 409", code)
	}
	_, view, _ := f.call(t, "GET", "/api/workflows/demo%2Fecho/expose", nil, nil)
	if raw, _ := json.Marshal(view); strings.Contains(string(raw), tok) || view["hook"].(map[string]any)["enabled"] != true {
		t.Fatalf("view = %s", raw)
	}

	// Wrong token, and the right token on the WRONG workflow.
	if code, _, _ := f.call(t, "POST", hook, body, map[string]string{"Authorization": "Bearer wfxe_wrong"}); code != 401 {
		t.Fatalf("wrong token = %d", code)
	}
	if code, _, _ := f.call(t, "POST", "/hooks/demo/other", body, auth); code != 401 {
		t.Fatalf("token on another workflow = %d, want 401", code)
	}
	if code, _, _ := f.call(t, "POST", hook, body, nil); code != 401 {
		t.Fatalf("no token = %d", code)
	}

	// The right token, waiting for the output.
	code, out, _ := f.call(t, "POST", hook+"?wait=1&timeout=20", body, auth)
	if code != 200 || out["status"] != "done" {
		t.Fatalf("hook wait = %d %v", code, out)
	}
	if o, _ := json.Marshal(out["output"]); !strings.Contains(string(o), "hello ana") {
		t.Fatalf("output = %s", o)
	}

	// HMAC: keyed with the signing secret (shown once), over ts.id.body.
	signed := func(key, id string, b []byte) map[string]string {
		ts := time.Now().Unix()
		return map[string]string{hookSignatureName: SignPayload(key, ts, id, b),
			hookTimestampName: strconv.FormatInt(ts, 10), hookEventIDName: id}
	}
	// The stored token hash is NOT a key: a database leak must not sign.
	if code, _, _ := f.call(t, "POST", hook, body, signed(store.HashExposeToken(tok), "e1", body)); code != 401 {
		t.Fatalf("hmac keyed with the token hash = %d, want 401", code)
	}
	if code, _, _ := f.call(t, "POST", hook, body, signed(tok, "e1", body)); code != 401 {
		t.Fatalf("hmac keyed with the token = %d, want 401", code)
	}
	hdr := signed(signing, "e1", body)
	if code, _, _ := f.call(t, "POST", hook, []byte(`{"who":"eve"}`), hdr); code != 401 {
		t.Fatalf("hmac over a different body = %d, want 401", code)
	}
	if code, _, _ := f.call(t, "POST", hook, body, hdr); code != 202 {
		t.Fatalf("hmac = %d, want 202", code)
	}

	// Rotate: the old token dies. Revoke: the new one dies too.
	tok2 := f.issue(t, "/api/workflows/demo%2Fecho/expose/hook", true)
	if code, _, _ := f.call(t, "POST", hook, body, auth); code != 401 {
		t.Fatalf("rotated-away token = %d", code)
	}
	if code, _, _ := f.call(t, "DELETE", "/api/workflows/demo%2Fecho/expose/hook", nil, nil); code != 200 {
		t.Fatalf("revoke = %d", code)
	}
	if code, _, _ := f.call(t, "POST", hook, body, map[string]string{"Authorization": "Bearer " + tok2}); code != 401 {
		t.Fatalf("revoked token = %d", code)
	}
}

func TestHookIdempotencyKeyStartsOneRun(t *testing.T) {
	f := exposeServer(t)
	tok := f.issue(t, "/api/workflows/demo%2Fecho/expose/hook", false)
	hdr := map[string]string{"Authorization": "Bearer " + tok, "Idempotency-Key": "evt-42"}
	code, first, _ := f.call(t, "POST", "/hooks/demo/echo", []byte(`{"who":"a"}`), hdr)
	if code != 202 {
		t.Fatalf("first = %d %v", code, first)
	}
	code, second, h := f.call(t, "POST", "/hooks/demo/echo", []byte(`{"who":"a"}`), hdr)
	if code != 200 || second["runId"] != first["runId"] || h.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %v (first %v)", code, second, first["runId"])
	}
	runs, _ := f.st.FindRuns(context.Background(), store.RunFilter{Workflow: "demo/echo", Limit: 10})
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	if !strings.HasPrefix(runs[0].TriggeredBy, "hook:") {
		t.Fatalf("triggeredBy = %q", runs[0].TriggeredBy)
	}
}

func TestHookRateLimit(t *testing.T) {
	l := newRateLimiter(60, 3)
	for i := 0; i < 3; i++ {
		if !l.allow("k") {
			t.Fatalf("request %d refused inside the burst", i)
		}
	}
	if l.allow("k") {
		t.Fatal("the fourth request inside a second was allowed")
	}
	if !l.allow("other") {
		t.Fatal("one key's limit spilled onto another")
	}
}

func mcpClient(t *testing.T, srv *httptest.Server, path, tok string) (*client.Client, error) {
	t.Helper()
	c, err := client.NewStreamableHttpClient(srv.URL+path, transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + tok}))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1"}
	if _, err := c.Initialize(ctx, req); err != nil {
		_ = c.Close()
		return nil, err
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, nil
}

func TestMCPListsOptedInWorkflowsAndCallsThem(t *testing.T) {
	f := exposeServer(t)
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	ctx := context.Background()

	wfTok := f.issue(t, "/api/workflows/demo%2Fecho/expose/mcp", false)
	// A hook token is not an MCP token.
	hookTok := f.issue(t, "/api/workflows/demo%2Fother/expose/hook", false)
	if _, err := mcpClient(t, srv, "/mcp/demo", hookTok); err == nil {
		t.Fatal("a hook token opened the MCP endpoint")
	}
	if _, err := mcpClient(t, srv, "/mcp/demo", "wfxe_wrong"); err == nil {
		t.Fatal("a wrong token opened the MCP endpoint")
	}
	// The echo token cannot reach /mcp/demo/other.
	if _, err := mcpClient(t, srv, "/mcp/demo/other", wfTok); err == nil {
		t.Fatal("a workflow token reached another workflow")
	}

	c, err := mcpClient(t, srv, "/mcp/demo/echo", wfTok)
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", list.Tools)
	}
	if raw, _ := json.Marshal(list.Tools[0]); !strings.Contains(string(raw), `"who"`) {
		t.Fatalf("input schema = %s", raw)
	}
	call := mcp.CallToolRequest{}
	call.Params.Name = "echo"
	call.Params.Arguments = map[string]any{"who": "bo"}
	res, err := c.CallTool(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("call errored: %+v", res)
	}
	if raw, _ := json.Marshal(res.StructuredContent); !strings.Contains(string(raw), "hello bo") || !strings.Contains(string(raw), `"done"`) {
		t.Fatalf("structured = %s", raw)
	}

	// A project token lists only the workflows that opted in (echo, not other).
	ptCode, pt, _ := f.call(t, "POST", "/api/projects/demo/expose/mcp", nil, nil)
	if ptCode != 201 {
		t.Fatalf("project token = %d %v", ptCode, pt)
	}
	pc, err := mcpClient(t, srv, "/mcp/demo", pt["token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	plist, err := pc.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plist.Tools) != 1 || plist.Tools[0].Name != "echo" {
		t.Fatalf("project tools = %+v", plist.Tools)
	}

	// Revoked: the endpoint refuses.
	f.call(t, "DELETE", "/api/workflows/demo%2Fecho/expose/mcp", nil, nil)
	if _, err := mcpClient(t, srv, "/mcp/demo/echo", wfTok); err == nil {
		t.Fatal("a revoked token opened the MCP endpoint")
	}
}
