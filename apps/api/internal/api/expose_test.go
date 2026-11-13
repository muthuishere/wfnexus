package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

	// HMAC: keyed with the signing secret (shown once), over the raw body.
	sign := func(key string, b []byte) string {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write(b)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	// The stored token hash is NOT a key: a database leak must not sign.
	if code, _, _ := f.call(t, "POST", hook, body, map[string]string{hookSignatureName: sign(store.HashExposeToken(tok), body)}); code != 401 {
		t.Fatalf("hmac keyed with the token hash = %d, want 401", code)
	}
	if code, _, _ := f.call(t, "POST", hook, body, map[string]string{hookSignatureName: sign(tok, body)}); code != 401 {
		t.Fatalf("hmac keyed with the token = %d, want 401", code)
	}
	sig := sign(signing, body)
	if code, _, _ := f.call(t, "POST", hook, body, map[string]string{hookSignatureName: sig}); code != 202 {
		t.Fatalf("hmac = %d, want 202", code)
	}
	if code, _, _ := f.call(t, "POST", hook, []byte(`{"who":"eve"}`), map[string]string{hookSignatureName: sig}); code != 401 {
		t.Fatalf("hmac over a different body = %d, want 401", code)
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
	if names := toolNames(list.Tools); names != "echo,wfx_run_status" {
		t.Fatalf("tools = %s", names)
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
	if names := toolNames(plist.Tools); names != "echo,wfx_run_status" {
		t.Fatalf("project tools = %s", names)
	}

	// Revoked: the endpoint refuses.
	f.call(t, "DELETE", "/api/workflows/demo%2Fecho/expose/mcp", nil, nil)
	if _, err := mcpClient(t, srv, "/mcp/demo/echo", wfTok); err == nil {
		t.Fatal("a revoked token opened the MCP endpoint")
	}
}

func toolNames(ts []mcp.Tool) string {
	var n []string
	for _, t := range ts {
		n = append(n, t.Name)
	}
	sort.Strings(n)
	return strings.Join(n, ",")
}

func callTool(t *testing.T, c *client.Client, name string, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{"isError": res.IsError}
	if raw, _ := json.Marshal(res.StructuredContent); string(raw) != "null" {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// wfx_wait: 0 answers with the run id at once; the default still waits, so
// existing callers see no change.
func TestMCPCallCanReturnTheRunIDAtOnce(t *testing.T) {
	f := exposeServer(t)
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	c, err := mcpClient(t, srv, "/mcp/demo/echo", f.issue(t, "/api/workflows/demo%2Fecho/expose/mcp", false))
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range list.Tools {
		if raw, _ := json.Marshal(tl); tl.Name == "echo" && !strings.Contains(string(raw), mcpWaitArg) {
			t.Fatalf("echo schema has no %s: %s", mcpWaitArg, raw)
		}
	}

	quick := callTool(t, c, "echo", map[string]any{"who": "bo", "wfx_wait": 0})
	id, _ := quick["runId"].(string)
	if id == "" || quick["settled"] == true || quick["isError"] == true {
		t.Fatalf("wfx_wait 0 = %v", quick)
	}
	// The argument does not reach the workflow's input.
	run, err := f.st.GetRun(context.Background(), uuid.MustParse(id))
	if err != nil || strings.Contains(string(run.Input), mcpWaitArg) {
		t.Fatalf("input = %s %v", run.Input, err)
	}
	// Status reports it until it finishes, with the result.
	deadline := time.Now().Add(20 * time.Second)
	var st map[string]any
	for time.Now().Before(deadline) {
		if st = callTool(t, c, "wfx_run_status", map[string]any{"run_id": id}); st["settled"] == true {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if raw, _ := json.Marshal(st); st["status"] != "done" || !strings.Contains(string(raw), "hello bo") {
		t.Fatalf("status = %s", raw)
	}

	// Default: waits for the result, as before.
	if def := callTool(t, c, "echo", map[string]any{"who": "al"}); def["status"] != "done" || def["settled"] != true {
		t.Fatalf("default call = %v", def)
	}
	if bad := callTool(t, c, "echo", map[string]any{"wfx_wait": "soon"}); bad["isError"] != true {
		t.Fatalf("bad wfx_wait = %v", bad)
	}
}

// The status tool answers only for runs this token could have started: another
// project's run id, a workflow the token does not reach, and an unknown id all
// get the same "run not found".
func TestMCPRunStatusIsConfinedToTheTokensWorkflows(t *testing.T) {
	f := exposeServer(t)
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	// A second project with a workflow of the same short name.
	repo := t.TempDir()
	wf := filepath.Join(repo, ".wfx", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "echo.yaml"), []byte(exposeEcho), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.ImportRepo(context.Background(), "beta", repo, ""); err != nil {
		t.Fatal(err)
	}
	f.issue(t, "/api/workflows/demo%2Fecho/expose/mcp", false)
	f.issue(t, "/api/workflows/beta%2Fecho/expose/mcp", false)
	tok := func(p string) string {
		code, out, _ := f.call(t, "POST", "/api/projects/"+p+"/expose/mcp", nil, nil)
		if code != 201 {
			t.Fatalf("project token %s = %d %v", p, code, out)
		}
		return out["token"].(string)
	}
	a, err := mcpClient(t, srv, "/mcp/demo", tok("demo"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := mcpClient(t, srv, "/mcp/beta", tok("beta"))
	if err != nil {
		t.Fatal(err)
	}
	aRun := callTool(t, a, "echo", map[string]any{"who": "a"})["runId"].(string)
	if st := callTool(t, a, "wfx_run_status", map[string]any{"run_id": aRun}); st["status"] != "done" {
		t.Fatalf("own run = %v", st)
	}
	// Project beta's token asks for project demo's run.
	if st := callTool(t, b, "wfx_run_status", map[string]any{"run_id": aRun}); st["isError"] != true || st["status"] != nil {
		t.Fatalf("beta read demo's run: %v", st)
	}
	// A run of a demo workflow the token does not reach (started over REST).
	code, out, _ := f.call(t, "POST", "/api/workflows/demo%2Fother/runs", []byte(`{}`), nil)
	if code != 201 {
		t.Fatalf("run other = %d %v", code, out)
	}
	otherRun := out["run"].(map[string]any)["id"].(string)
	if st := callTool(t, a, "wfx_run_status", map[string]any{"run_id": otherRun}); st["isError"] != true {
		t.Fatalf("read a run of an unexposed workflow: %v", st)
	}
	for _, id := range []string{uuid.NewString(), "nope"} {
		if st := callTool(t, a, "wfx_run_status", map[string]any{"run_id": id}); st["isError"] != true {
			t.Fatalf("id %s = %v", id, st)
		}
	}
}

// A workflow whose input uses the reserved wfx_wait name is not offered.
func TestMCPSkipsWorkflowsUsingReservedNames(t *testing.T) {
	def := &workflow.Definition{Name: "demo/x", InputSchema: map[string]any{"type": "object",
		"properties": map[string]any{"wfx_wait": map[string]any{"type": "string"}}}}
	if !mcpReserved("demo", def) {
		t.Fatal("wfx_wait input not treated as reserved")
	}
	if !mcpReserved("demo", &workflow.Definition{Name: "demo/wfx_run_status"}) {
		t.Fatal("a workflow named like the status tool not treated as reserved")
	}
	if mcpReserved("demo", &workflow.Definition{Name: "demo/echo"}) {
		t.Fatal("a plain workflow treated as reserved")
	}
}
