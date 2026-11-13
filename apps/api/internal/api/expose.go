package api

// Inbound exposure (ADR 0027): a workflow can be started from OUTSIDE by a
// hook (POST /hooks/{project}/{workflow}) or by an MCP client
// (/mcp/{project}[/{workflow}]). Both are off until a person issues a token,
// both answer exactly one workflow (or one project's opted-in workflows), and
// both sit outside /api on purpose: their credential is the per-workflow
// token, never a user session.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

const (
	hookMaxBody       = 1 << 20 // 1 MiB of JSON is a payload, more is an attack
	defaultWait       = 30 * time.Second
	maxWait           = 120 * time.Second
	exposeRatePerMin  = 60
	exposeRateBurst   = 20
	hookSignatureName = "X-WFX-Signature"
	hookTimestampName = "X-WFX-Timestamp"
	hookEventIDName   = "X-WFX-Event-Id"
	// hookWindow is how far a signed hook's timestamp may be from now. A
	// captured request is dead after it; inside it the event id refuses a
	// second delivery.
	hookWindow = 5 * time.Minute
)

// SignPayload is the one signature both directions use: HMAC-SHA256 over
// `<timestamp>.<event id>.<body>`, as `sha256=<hex>`. Binding the time and
// the id into the MAC is what makes the window and the dedupe key hold.
func SignPayload(secret string, ts int64, eventID string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s.", ts, eventID)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// exposeWaitDefault is how long a tools/call (or ?wait=1) waits for a run
// before answering with its id. A var so tests can shorten it.
var exposeWaitDefault = defaultWait

// ---------------------------------------------------------------------------
// Rate limiting: a token bucket per key (the token id once authenticated, the
// client address before). Small and in memory — a limit, not an accounting.

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	perMin  float64
	burst   float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMin, burst int) *rateLimiter {
	return &rateLimiter{buckets: map[string]*bucket{}, perMin: float64(perMin), burst: float64(burst)}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) > 10000 { // never grow without bound
			l.buckets = map[string]*bucket{}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Minutes() * l.perMin
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------------------
// Names. A workflow is stored as its full name ("demo/hello", or "hello" in
// the local project); the URL carries the project and the SHORT name.

func shortName(project, full string) string {
	return strings.TrimPrefix(full, project+"/")
}

// resolveExposed finds the workflow a public URL names, or "".
func (s *Server) resolveExposed(project, wf string) string {
	defs := s.eng.Definitions()
	for _, cand := range []string{project + "/" + wf, wf} {
		if defs[cand] != nil && s.eng.ProjectFor(cand) == project {
			return cand
		}
	}
	return ""
}

var toolNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func toolName(project, full string) string {
	return toolNameUnsafe.ReplaceAllString(shortName(project, full), "_")
}

func exposeBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func exposeUnauthorized(w http.ResponseWriter) {
	// One answer for a missing, wrong, revoked or misdirected token, so a
	// caller learns nothing about which workflows exist or are exposed.
	w.Header().Set("WWW-Authenticate", `Bearer realm="wfnexus"`)
	writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
}

// ---------------------------------------------------------------------------
// Starting and waiting — the same admission path as POST /api/workflows/{name}/runs.

func (s *Server) startExposedRun(ctx context.Context, name string, input json.RawMessage, by string) (uuid.UUID, error) {
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	input, err := s.eng.PrepareInput(name, input)
	if err != nil {
		return uuid.Nil, err
	}
	run, err := s.eng.CreateRunAdmitted(ctx, name, input, false)
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.store.SetTriggeredBy(ctx, run.ID, by); err != nil {
		return uuid.Nil, err
	}
	s.eng.Start(run.ID)
	return run.ID, nil
}

// runSettled is a status the run will not leave on its own.
func runSettled(status string) bool {
	switch status {
	case "done", "failed", "cancelled", "awaiting_approval", "needs_input":
		return true
	}
	return false
}

type exposedResult struct {
	RunID   string          `json:"runId"`
	Status  string          `json:"status"`
	Output  json.RawMessage `json:"output,omitempty"`
	Outcome string          `json:"outcome,omitempty"`
	Error   string          `json:"error,omitempty"`
	Settled bool            `json:"settled"`
}

// waitRun polls until the run settles or the wait runs out, and answers with
// the last finished step's output.
func (s *Server) waitRun(ctx context.Context, id uuid.UUID, wait time.Duration) (exposedResult, error) {
	deadline := time.Now().Add(wait)
	for {
		run, err := s.store.GetRun(ctx, id)
		if err != nil {
			return exposedResult{}, err
		}
		res := exposedResult{RunID: id.String(), Status: run.Status, Outcome: run.Outcome, Error: run.Error}
		if runSettled(run.Status) || time.Now().After(deadline) {
			res.Settled = runSettled(run.Status)
			if steps, err := s.store.ListSteps(ctx, id); err == nil {
				sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
				for i := len(steps) - 1; i >= 0; i-- {
					if steps[i].Status == "done" && len(steps[i].Output) > 0 && string(steps[i].Output) != "null" {
						res.Output = steps[i].Output
						break
					}
				}
			}
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, nil
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func waitFor(r *http.Request) time.Duration {
	if v := r.URL.Query().Get("timeout"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			d := time.Duration(n) * time.Second
			if d > maxWait {
				d = maxWait
			}
			return d
		}
	}
	return exposeWaitDefault
}

// ---------------------------------------------------------------------------
// The hook.

func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	if !s.exposeLimit.allow("ip:" + clientAddr(r)) {
		writeErr(w, http.StatusTooManyRequests, errors.New("rate limited"))
		return
	}
	project, wf := urlName(r, "project"), urlName(r, "workflow")
	body, err := io.ReadAll(io.LimitReader(r.Body, hookMaxBody+1))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if len(body) > hookMaxBody {
		writeErr(w, http.StatusRequestEntityTooLarge, errors.New("body too large"))
		return
	}
	name := s.resolveExposed(project, wf)
	if name == "" {
		exposeUnauthorized(w)
		return
	}
	tokenID, eventID, ok := s.hookAuth(r, project, name, body)
	if !ok {
		exposeUnauthorized(w)
		return
	}
	if !s.exposeLimit.allow("tok:" + tokenID) {
		writeErr(w, http.StatusTooManyRequests, errors.New("rate limited"))
		return
	}
	s.store.TouchExposeToken(r.Context(), tokenID)
	if len(strings.TrimSpace(string(body))) > 0 && !json.Valid(body) {
		writeErr(w, 400, errors.New("body must be JSON"))
		return
	}

	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 200 {
		writeErr(w, 400, errors.New("Idempotency-Key is longer than 200 characters"))
		return
	}
	// A signed event is delivered once: its id (inside the MAC) is the key,
	// and a second delivery is refused rather than replayed.
	if eventID != "" {
		key = "event:" + eventID
	}
	var id uuid.UUID
	replay := false
	if key != "" {
		claimed, prior, err := s.store.ReserveIdempotencyKey(r.Context(), project, name, key)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		if !claimed {
			if eventID != "" {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "duplicate event: " + eventID + " was already received", "runId": prior})
				return
			}
			if prior == "" {
				writeErr(w, http.StatusConflict, errors.New("a request with this Idempotency-Key is still starting"))
				return
			}
			id, _ = uuid.Parse(prior)
			replay = true
		}
	}
	if !replay {
		id, err = s.startExposedRun(r.Context(), name, body, "hook:"+tokenID)
		if err != nil {
			if key != "" {
				s.store.ReleaseIdempotencyKey(r.Context(), project, name, key)
			}
			if errors.Is(err, engine.ErrConcurrencyBusy) {
				writeErr(w, http.StatusConflict, err)
				return
			}
			writeErr(w, 400, err)
			return
		}
		if key != "" {
			if err := s.store.SetIdempotencyRun(r.Context(), project, name, key, id.String()); err != nil {
				log.Printf("hook: recording idempotency key for %s: %v", name, err)
			}
		}
	}
	if replay {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	wait := r.URL.Query().Get("wait")
	if wait == "1" || wait == "true" {
		res, err := s.waitRun(r.Context(), id, waitFor(r))
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		code := http.StatusOK
		if !res.Settled {
			code = http.StatusAccepted
		}
		writeJSON(w, code, res)
		return
	}
	code := http.StatusAccepted
	if replay {
		code = http.StatusOK
	}
	run, err := s.store.GetRun(r.Context(), id)
	status := ""
	if err == nil {
		status = run.Status
	}
	writeJSON(w, code, exposedResult{RunID: id.String(), Status: status, Settled: runSettled(status)})
}

// hookAuth accepts a bearer token issued for THIS workflow's hook, or a signed
// request: X-WFX-Signature = SignPayload(signing secret, X-WFX-Timestamp,
// X-WFX-Event-Id, body), keyed with the hook's SIGNING SECRET — a separate
// random secret shown once at issue and stored sealed with the server's
// env-store key. The stored token hash is never a key. eventID is the signed
// event id ("" for a bearer request).
func (s *Server) hookAuth(r *http.Request, project, name string, body []byte) (tokenID, eventID string, ok bool) {
	if tok := exposeBearer(r); tok != "" {
		t, err := s.store.LookupExposeToken(r.Context(), tok)
		if err != nil || t.Kind != store.ExposeHook || t.Project != project || t.Workflow != name {
			return "", "", false
		}
		return t.ID, "", true
	}
	sig := r.Header.Get(hookSignatureName)
	eventID = strings.TrimSpace(r.Header.Get(hookEventIDName))
	ts, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(hookTimestampName)), 10, 64)
	if sig == "" || eventID == "" || len(eventID) > 200 || err != nil {
		return "", "", false
	}
	if d := time.Since(time.Unix(ts, 0)); d > hookWindow || d < -hookWindow {
		return "", "", false // outside the replay window
	}
	box := s.eng.Secrets()
	if box == nil {
		return "", "", false // no server key: signed hooks are off, bearer only
	}
	sealed, err := s.store.ActiveExposeSigning(r.Context(), project, name, store.ExposeHook)
	if err != nil {
		return "", "", false
	}
	for id, sl := range sealed {
		key, err := box.Open(sl)
		if err != nil {
			continue
		}
		if hmac.Equal([]byte(SignPayload(key, ts, eventID, body)), []byte(sig)) {
			return id, eventID, true
		}
	}
	return "", "", false
}

// newSigningSecret is a hook's HMAC key: 32 random bytes, hex, with a prefix
// that says what it is.
func newSigningSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "wfxs_" + hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// MCP: a stateless streamable-HTTP server built per request, holding exactly
// the tools this token may call.

func (s *Server) mcpEndpoint(w http.ResponseWriter, r *http.Request) {
	if !s.exposeLimit.allow("ip:" + clientAddr(r)) {
		writeErr(w, http.StatusTooManyRequests, errors.New("rate limited"))
		return
	}
	project, only := urlName(r, "project"), urlName(r, "workflow")
	tok := exposeBearer(r)
	if tok == "" {
		exposeUnauthorized(w)
		return
	}
	t, err := s.store.LookupExposeToken(r.Context(), tok)
	if err != nil || t.Kind != store.ExposeMCP || t.Project != project {
		exposeUnauthorized(w)
		return
	}
	if !s.exposeLimit.allow("tok:" + t.ID) {
		writeErr(w, http.StatusTooManyRequests, errors.New("rate limited"))
		return
	}
	// What this token reaches: its own workflow, or — a project token — every
	// workflow of the project that has opted in with its own mcp token.
	var allowed []string
	if t.Workflow != "" {
		allowed = []string{t.Workflow}
	} else {
		act, err := s.store.ActiveExposeTokens(r.Context(), project, nil)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		for _, a := range act {
			if a.Kind == store.ExposeMCP && a.Workflow != "" {
				allowed = append(allowed, a.Workflow)
			}
		}
	}
	if only != "" {
		name := s.resolveExposed(project, only)
		ok := false
		for _, a := range allowed {
			ok = ok || (name != "" && a == name)
		}
		if !ok {
			exposeUnauthorized(w)
			return
		}
		allowed = []string{name}
	}
	s.store.TouchExposeToken(r.Context(), t.ID)

	srv := mcpserver.NewMCPServer("wfnexus-"+project, "1", mcpserver.WithToolCapabilities(false))
	defs := s.eng.Definitions()
	for _, name := range allowed {
		def := defs[name]
		if def == nil {
			continue // exposed, then deleted: not a tool any more
		}
		srv.AddTool(mcpTool(project, def), s.mcpCall(name, t.ID))
	}
	mcpserver.NewStreamableHTTPServer(srv, mcpserver.WithStateLess(true)).ServeHTTP(w, r)
}

func mcpTool(project string, def *workflow.Definition) mcp.Tool {
	schema := def.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	if _, ok := schema["type"]; !ok {
		cp := map[string]any{"type": "object"}
		for k, v := range schema {
			cp[k] = v
		}
		schema = cp
	}
	raw, _ := json.Marshal(schema)
	desc := def.Description
	if desc == "" {
		desc = "Run the wfnexus workflow " + def.Name
	}
	return mcp.NewToolWithRawSchema(toolName(project, def.Name), desc, raw)
}

func (s *Server) mcpCall(name, tokenID string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input, _ := json.Marshal(req.GetRawArguments())
		id, err := s.startExposedRun(ctx, name, input, "mcp:"+tokenID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := s.waitRun(ctx, id, exposeWaitDefault)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		text, _ := json.Marshal(res)
		out := mcp.NewToolResultStructured(res, string(text))
		if !res.Settled {
			out.Content = []mcp.Content{mcp.NewTextContent(fmt.Sprintf(
				"run %s is still %s; it continues in the background. Result so far: %s", res.RunID, res.Status, text))}
		}
		out.IsError = res.Status == "failed" || res.Status == "cancelled"
		return out, nil
	}
}

// ---------------------------------------------------------------------------
// Management, under /api with the ordinary permissions: workflows:write to
// expose a workflow, projects:write for a project token.

type exposeKindView struct {
	Enabled    bool       `json:"enabled"`
	Path       string     `json:"path"`
	URL        string     `json:"url"`
	Prefix     string     `json:"prefix,omitempty"`
	CreatedAt  *time.Time `json:"createdAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func exposePath(kind, project, short string) string {
	if kind == store.ExposeHook {
		return "/hooks/" + project + "/" + short
	}
	if short == "" {
		return "/mcp/" + project
	}
	return "/mcp/" + project + "/" + short
}

func (s *Server) exposeView(r *http.Request, project, name string) (map[string]any, error) {
	act, err := s.store.ActiveExposeTokens(r.Context(), project, &name)
	if err != nil {
		return nil, err
	}
	short := shortName(project, name)
	out := map[string]any{"workflow": name, "project": project, "tool": toolName(project, name)}
	for _, kind := range []string{store.ExposeHook, store.ExposeMCP} {
		p := exposePath(kind, project, short)
		v := exposeKindView{Path: p, URL: baseURL(r) + p}
		for _, a := range act {
			if a.Kind == kind {
				created := a.CreatedAt
				v.Enabled, v.Prefix, v.CreatedAt, v.LastUsedAt = true, a.Prefix, &created, a.LastUsedAt
			}
		}
		out[kind] = v
	}
	out["projectMcp"] = map[string]any{"path": exposePath(store.ExposeMCP, project, ""), "url": baseURL(r) + exposePath(store.ExposeMCP, project, "")}
	return out, nil
}

func exposeKind(w http.ResponseWriter, r *http.Request) (string, bool) {
	k := chi.URLParam(r, "kind")
	if k != store.ExposeHook && k != store.ExposeMCP {
		writeErr(w, 400, errors.New("kind must be hook or mcp"))
		return "", false
	}
	return k, true
}

func (s *Server) getExpose(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.workflowInScope(w, r, name) {
		return
	}
	if s.eng.Definitions()[name] == nil {
		writeErr(w, 404, errors.New("workflow not found"))
		return
	}
	v, err := s.exposeView(r, s.eng.ProjectFor(name), name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}

// mcpSnippet is what a person pastes into an MCP client's config.
func mcpSnippet(url, token string) map[string]any {
	return map[string]any{"mcpServers": map[string]any{"wfnexus": map[string]any{
		"type": "http", "url": url, "headers": map[string]string{"Authorization": "Bearer " + token},
	}}}
}

func (s *Server) issueExpose(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.workflowInScope(w, r, name) {
		return
	}
	kind, ok := exposeKind(w, r)
	if !ok {
		return
	}
	if s.eng.Definitions()[name] == nil {
		writeErr(w, 404, errors.New("workflow not found"))
		return
	}
	var body struct {
		Rotate bool `json:"rotate"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	project := s.eng.ProjectFor(name)
	act, err := s.store.ActiveExposeTokens(r.Context(), project, &name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	for _, a := range act {
		if a.Kind == kind && !body.Rotate {
			writeErr(w, http.StatusConflict, fmt.Errorf("%s is already exposed as %s; rotate to get a new token", name, kind))
			return
		}
	}
	tok, secret, err := s.store.IssueExposeToken(r.Context(), project, name, kind)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	signing := ""
	if kind == store.ExposeHook {
		if signing, err = s.issueSigning(r.Context(), tok.ID); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	v, err := s.exposeView(r, project, name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	v["token"] = secret
	if signing != "" {
		v["signingSecret"] = signing
	} else if kind == store.ExposeHook {
		v["signingNote"] = "the server has no secret key (WFX_SECRET_KEY), so signed hooks are off: use the bearer token"
	}
	view := v[kind].(exposeKindView)
	if kind == store.ExposeMCP {
		v["snippet"] = mcpSnippet(view.URL, secret)
	} else {
		v["snippet"] = fmt.Sprintf("curl -X POST '%s?wait=1' -H 'Authorization: Bearer %s' -H 'Content-Type: application/json' -d '{}'", view.URL, secret)
	}
	writeJSON(w, 201, v)
}

// issueSigning makes a hook token's HMAC signing secret, stores it sealed and
// returns the plaintext (shown once). No server key: "" and no error — the
// hook still works with its bearer token.
func (s *Server) issueSigning(ctx context.Context, tokenID string) (string, error) {
	box := s.eng.Secrets()
	if box == nil {
		return "", nil
	}
	secret, err := newSigningSecret()
	if err != nil {
		return "", err
	}
	sealed, err := box.Seal(secret)
	if err != nil {
		return "", err
	}
	if err := s.store.SetExposeSigning(ctx, tokenID, sealed); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *Server) revokeExpose(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.workflowInScope(w, r, name) {
		return
	}
	kind, ok := exposeKind(w, r)
	if !ok {
		return
	}
	n, err := s.store.RevokeExposeTokens(r.Context(), s.eng.ProjectFor(name), name, kind)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "revoked": n})
}

// The project-wide MCP token: one config entry for every opted-in workflow.
func (s *Server) getProjectExpose(w http.ResponseWriter, r *http.Request) {
	project := urlName(r, "name")
	if !s.inScope(r, project) {
		notFound(w)
		return
	}
	act, err := s.store.ActiveExposeTokens(r.Context(), project, nil)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	p := exposePath(store.ExposeMCP, project, "")
	writeJSON(w, 200, map[string]any{"project": project, "path": p, "url": baseURL(r) + p, "tokens": act})
}

func (s *Server) issueProjectExpose(w http.ResponseWriter, r *http.Request) {
	project := urlName(r, "name")
	if !s.inScope(r, project) {
		notFound(w)
		return
	}
	if chi.URLParam(r, "kind") != store.ExposeMCP {
		writeErr(w, 400, errors.New("a project token is mcp only; hooks are per workflow"))
		return
	}
	_, secret, err := s.store.IssueExposeToken(r.Context(), project, "", store.ExposeMCP)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	url := baseURL(r) + exposePath(store.ExposeMCP, project, "")
	writeJSON(w, 201, map[string]any{"project": project, "url": url, "token": secret, "snippet": mcpSnippet(url, secret)})
}

func (s *Server) revokeProjectExpose(w http.ResponseWriter, r *http.Request) {
	project := urlName(r, "name")
	if !s.inScope(r, project) {
		notFound(w)
		return
	}
	n, err := s.store.RevokeExposeTokens(r.Context(), project, "", store.ExposeMCP)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "revoked": n})
}
