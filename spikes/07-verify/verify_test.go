// Package verify is the CONSUMER-side verification of toolnexus issues #87-#93.
//
// It builds against the local toolnexus working tree (see the replace directive
// in ../go.mod), so it tests the fix branch as it is edited. Everything here is
// offline: the LLM is a scripted http.RoundTripper, so a failure is a real
// behavioural regression and never a flaky network.
package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"
)

// ---- scripted wire --------------------------------------------------------

type reply struct {
	tool string
	args map[string]any
	text string
}

type wire struct {
	mu       sync.Mutex
	replies  []reply
	n        int
	requests []map[string]any
	delay    time.Duration
}

func (w *wire) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)

	w.mu.Lock()
	w.requests = append(w.requests, req)
	var rp reply
	if w.n < len(w.replies) {
		rp = w.replies[w.n]
		w.n++
	} else {
		rp = reply{text: "done"}
	}
	delay := w.delay
	w.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}

	msg := map[string]any{"role": "assistant"}
	if rp.tool != "" {
		raw, _ := json.Marshal(rp.args)
		msg["content"] = nil
		msg["tool_calls"] = []any{map[string]any{
			"id": fmt.Sprintf("c%d", w.n), "type": "function",
			"function": map[string]any{"name": rp.tool, "arguments": string(raw)},
		}}
	} else {
		msg["content"] = rp.text
	}
	out, _ := json.Marshal(map[string]any{
		"id": "x", "model": "scripted",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	return &http.Response{
		StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(out))),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func (w *wire) sysPrompt(i int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if i >= len(w.requests) {
		return ""
	}
	msgs, _ := w.requests[i]["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] == "system" {
			s, _ := mm["content"].(string)
			return s
		}
	}
	return ""
}

func (w *wire) model(i int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if i >= len(w.requests) {
		return ""
	}
	s, _ := w.requests[i]["model"].(string)
	return s
}

func (w *wire) toolResults() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, req := range w.requests {
		msgs, _ := req["messages"].([]any)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if mm["role"] == "tool" {
				if c, ok := mm["content"].(string); ok {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

func echoTool(calls *[]string) tn.Tool {
	return tn.NativeTool("shell", "Run a shell command.",
		tn.JSONSchema{"type": "object", "properties": map[string]any{
			"command": map[string]any{"type": "string"}}, "required": []string{"command"},
			"additionalProperties": false},
		func(_ context.Context, args map[string]any) (string, error) {
			cmd, _ := args["command"].(string)
			*calls = append(*calls, cmd)
			return "ran: " + cmd, nil
		})
}

func toolkit(t *testing.T, tools ...tn.Tool) *tn.Toolkit {
	t.Helper()
	tk, err := tn.CreateToolkit(context.Background(), tn.Options{Builtins: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tk.Close)
	return tk.Register(tools...)
}

// ---- #87 -------------------------------------------------------------------

// A guardrail declared on the Spec must DENY on the Loop path, not only on the
// runtime path. A policy enforced through one door and not the other is a hole.
func TestIssue87_LoopEnforcesSpecGuardrails(t *testing.T) {
	var ran []string
	tk := toolkit(t, echoTool(&ran))
	w := &wire{replies: []reply{
		{tool: "shell", args: map[string]any{"command": "git push origin main"}},
		{text: "understood, I will not push"},
	}}

	ag := agents.New("ops", agents.Spec{
		Does: "operates",
		Guardrails: []agents.Guardrail{func(ev tn.BeforeToolEvent) string {
			if cmd, _ := ev.Args["command"].(string); strings.Contains(cmd, "git push") {
				return "publishing is owner-gated"
			}
			return ""
		}},
	})
	out, err := ag.Loop(tn.ClientOptions{
		BaseURL: "http://scripted.invalid/v1", Style: tn.StyleOpenAI, Model: "m",
		APIKey: "x", HTTPClient: &http.Client{Transport: w},
	}, tk).Run(context.Background(), "push it", agents.RunOpts{})
	if err != nil {
		t.Fatal(err)
	}

	if len(ran) != 0 {
		t.Fatalf("SECURITY: the guardrail did not fire on the Loop path — the tool ran %v", ran)
	}
	var sawDenial bool
	for _, r := range w.toolResults() {
		if strings.Contains(r, "publishing is owner-gated") {
			sawDenial = true
		}
	}
	if !sawDenial {
		t.Fatalf("the model was never shown the denial reason; tool results = %v", w.toolResults())
	}
	if out.Status != "done" {
		t.Fatalf("a denial should not break the run: status=%s", out.Status)
	}
}

func TestIssue87_LoopHonoursSoulModelAndBudget(t *testing.T) {
	tk := toolkit(t)
	w := &wire{replies: []reply{{text: "ok"}}}
	ag := agents.New("writer", agents.Spec{
		Does: "writes", Soul: "SOUL-MARKER: you are terse.",
		Model: "spec-model", Budget: &agents.Budget{MaxTurns: 3},
	})
	if _, err := ag.Loop(tn.ClientOptions{
		BaseURL: "http://scripted.invalid/v1", Style: tn.StyleOpenAI,
		APIKey: "x", HTTPClient: &http.Client{Transport: w},
	}, tk).Run(context.Background(), "hi", agents.RunOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := w.sysPrompt(0); !strings.Contains(got, "SOUL-MARKER") {
		t.Fatalf("Spec.Soul did not reach the system prompt: %q", got)
	}
	if got := w.model(0); got != "spec-model" {
		t.Fatalf("Spec.Model ignored on the Loop path: %q", got)
	}
}

// A caller-supplied SystemPrompt must still win — the caller built these
// options on purpose.
func TestIssue87_CallerSystemPromptWins(t *testing.T) {
	tk := toolkit(t)
	w := &wire{replies: []reply{{text: "ok"}}}
	ag := agents.New("writer", agents.Spec{Does: "writes", Soul: "SOUL-MARKER"})
	if _, err := ag.Loop(tn.ClientOptions{
		BaseURL: "http://scripted.invalid/v1", Style: tn.StyleOpenAI, Model: "m", APIKey: "x",
		SystemPrompt: "CALLER-MARKER", HTTPClient: &http.Client{Transport: w},
	}, tk).Run(context.Background(), "hi", agents.RunOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := w.sysPrompt(0); !strings.Contains(got, "CALLER-MARKER") {
		t.Fatalf("caller SystemPrompt was overridden by the soul: %q", got)
	}
}

// A driver genuinely cannot carry Tools/Team/WaitFor/OnMetric, so those are
// reported additively rather than silently dropped. A Loop-driven agent cannot
// delegate at all — by design, now discoverable.
func TestIssue87_UnsupportedIsReportedNotSwallowed(t *testing.T) {
	child := agents.New("explore", agents.Spec{Does: "reads"})
	ag := agents.New("lead", agents.Spec{
		Does: "delegates", Team: []*agents.Agent{child},
		Tools:    []tn.Tool{echoTool(new([]string))},
		WaitFor:  func(tn.Request) (tn.Answer, error) { return tn.Answer{}, nil },
		OnMetric: func(tn.MetricEvent) {},
	})
	got := agents.LoopUnsupported(ag.Spec)
	for _, want := range []string{"tools", "team", "waitFor", "onMetric"} {
		if !contains(got, want) {
			t.Fatalf("#87 LoopUnsupported did not report %q: %v", want, got)
		}
	}
	// and a Spec a driver CAN fully honour reports nothing
	plain := agents.New("writer", agents.Spec{Does: "writes", Soul: "s", Model: "m"})
	if u := agents.LoopUnsupported(plain.Spec); len(u) != 0 {
		t.Fatalf("#87 a fully-honourable Spec should report nothing unsupported: %v", u)
	}
	t.Logf("#87 Loop cannot carry: %v", got)
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// ---- #88 / #90 -------------------------------------------------------------

// TotalTokens must be the whole subtree, and Limit must name the stop.
func TestIssue88and90_SubtreeTokensAndLimit(t *testing.T) {
	var ran []string
	childTk := toolkit(t, echoTool(&ran))
	w := &wire{replies: []reply{
		{tool: "task", args: map[string]any{"agent": "explore", "prompt": "look"}},
		{tool: "shell", args: map[string]any{"command": "grep x"}}, // the child's turn
		{text: "found it"},
		{text: "the child found it"},
	}}

	child := agents.New("explore", agents.Spec{Does: "reads code", Tools: childTk.Tools()})
	lead := agents.New("lead", agents.Spec{Does: "delegates", Team: []*agents.Agent{child}})

	res, rt := lead.Run(agents.Options{
		Transport: w,
		LLM:       &agents.LLMOptions{BaseURL: "http://scripted.invalid/v1", Style: tn.StyleOpenAI, APIKey: "x", Model: "m"},
	}, "diagnose")

	if res.Status != "done" {
		t.Fatalf("status = %s (%s)", res.Status, res.Text)
	}
	if len(ran) == 0 {
		t.Fatal("the child never ran — delegation did not happen")
	}
	tree := rt.UsageTokens(rt.Root)
	if res.TotalTokens < tree {
		t.Fatalf("#88 TotalTokens=%d is below the tree total %d — the subtree is still uncounted",
			res.TotalTokens, tree)
	}
	if res.OwnTokens <= 0 || res.OwnTokens > res.TotalTokens {
		t.Fatalf("#88 OwnTokens=%d is not a sane own-figure against TotalTokens=%d", res.OwnTokens, res.TotalTokens)
	}
	if res.TotalTokens == res.OwnTokens {
		t.Fatalf("#88 TotalTokens == OwnTokens (%d) although a child ran — the subtree is not rolled up", res.OwnTokens)
	}
}

// A budget stop must name its limit rather than making the host string-match.
func TestIssue90_LimitIsPopulatedOnABudgetStop(t *testing.T) {
	var ran []string
	tk := toolkit(t, echoTool(&ran))
	// never stops calling the tool, so MaxTurns must cut it off
	w := &wire{replies: []reply{
		{tool: "shell", args: map[string]any{"command": "a"}},
		{tool: "shell", args: map[string]any{"command": "b"}},
		{tool: "shell", args: map[string]any{"command": "c"}},
		{tool: "shell", args: map[string]any{"command": "d"}},
	}}
	ag := agents.New("spinner", agents.Spec{
		Does: "spins", Tools: tk.Tools(), Budget: &agents.Budget{MaxTurns: 2},
	})
	res, _ := ag.Run(agents.Options{
		Transport: w,
		LLM:       &agents.LLMOptions{BaseURL: "http://scripted.invalid/v1", Style: tn.StyleOpenAI, APIKey: "x", Model: "m"},
	}, "go")

	if res.Status != "incomplete" {
		t.Fatalf("a turn-cap stop should be incomplete, got %q (%s)", res.Status, res.Text)
	}
	if res.Limit == "" {
		t.Fatal("#90 TaskResult.Limit is empty — the host must string-match to learn why it stopped")
	}
	if res.Limit != "maxTurns" {
		t.Fatalf("#90 Limit = %q, want maxTurns", res.Limit)
	}
}

// Runtime.Resume now returns a usable result. Transcript replay is DEFERRED by
// decision — SPEC pins rewind-to-checkpoint by name — so a resumed leaf re-runs
// its own tools and the host must keep steps idempotent. We do: our answer
// path re-runs the step rather than calling Resume.
func TestIssue90_ResumeReturnsAResult(t *testing.T) {
	toolRuns := 0
	ask := tn.Tool{
		Name: "ask_human", Description: "Ask the operator.", Source: tn.SourceCustom,
		InputSchema: tn.JSONSchema{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		Execute: func(_ map[string]any, tc *tn.ToolContext) (tn.ToolResult, error) {
			toolRuns++
			if tc != nil && tc.Answer != nil {
				out, _ := tc.Answer.Data[tn.RelayOutputKey].(string)
				return tn.ToolResult{Output: "answered: " + out}, nil
			}
			return tn.Pending(tn.Request{ID: "r1", Kind: "input", Prompt: "which env?"}), nil
		},
	}
	tk := toolkit(t, ask)
	w := &wire{replies: []reply{
		{tool: "ask_human", args: map[string]any{}},
		{text: "thanks"},
		{tool: "ask_human", args: map[string]any{}}, // the replayed turn
		{text: "thanks"},
	}}
	ag := agents.New("asker", agents.Spec{Does: "asks", Tools: tk.Tools()})
	res, rt := ag.Run(agents.Options{
		Transport: w,
		LLM:       &agents.LLMOptions{BaseURL: "http://scripted.invalid/v1", Style: tn.StyleOpenAI, APIKey: "x", Model: "m"},
	}, "ask me")
	if res.Status != "pending" || res.Pending == nil {
		t.Fatalf("expected a durable halt, got %q", res.Status)
	}
	runsBefore := toolRuns

	// the point of the fix: a resumed run hands back a usable result
	out, err := rt.Resume(tn.AnswerOutput(res.Pending.ID, "staging"))
	if err != nil {
		t.Fatalf("#90 Resume errored: %v", err)
	}
	if out.Status == "" {
		t.Fatal("#90 Resume returned no usable TaskResult")
	}
	t.Logf("#90 resume => status=%q turns=%d totalTokens=%d", out.Status, out.Turns, out.TotalTokens)

	// Replay is DEFERRED by decision (SPEC pins rewind-to-checkpoint), so the
	// leaf's own tools re-run. Recorded, not asserted as a bug — it is why our
	// answer path re-runs the whole step instead of calling Resume.
	t.Logf("#90 tool invocations: %d before resume, %d after — leaf tools are NOT idempotent-protected; "+
		"SPEC's reattachment-by-task-key covers `task` calls only", runsBefore, toolRuns)
}

// ---- #89 -------------------------------------------------------------------

// Resuming with an Answer whose payload is under the wrong key must be an
// ERROR, not a silent "done" with the human's answer discarded.
func TestIssue89_WrongAnswerKeyIsNotSilent(t *testing.T) {
	asked := 0
	ask := tn.Tool{
		Name: "ask_human", Description: "Ask the operator.", Source: tn.SourceCustom,
		InputSchema: tn.JSONSchema{"type": "object", "properties": map[string]any{
			"q": map[string]any{"type": "string"}}, "additionalProperties": false},
		Execute: func(args map[string]any, tc *tn.ToolContext) (tn.ToolResult, error) {
			if tc != nil && tc.Answer != nil {
				out, _ := tc.Answer.Data[tn.RelayOutputKey].(string)
				return tn.ToolResult{Output: "answered: " + out}, nil
			}
			asked++
			return tn.Pending(tn.Request{ID: "r1", Kind: "input", Prompt: "which env?"}), nil
		},
	}
	tk := toolkit(t, ask)
	w := &wire{replies: []reply{
		{tool: "ask_human", args: map[string]any{"q": "which env?"}},
		{text: "thanks"},
	}}
	c := tn.CreateClient(tn.ClientOptions{
		BaseURL: "http://scripted.invalid/v1", Style: tn.StyleOpenAI, Model: "m", APIKey: "x",
		HTTPClient: &http.Client{Transport: w},
	})
	res, err := c.Run(context.Background(), "ask me", tk)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pending" || res.Pending == nil {
		t.Fatalf("expected a durable halt, got %q", res.Status)
	}

	// the host makes the classic mistake: a plausible but unread key
	bad := tn.Answer{ID: res.Pending.ID, Ok: true, Data: map[string]any{"value": "staging"}}
	out, err := c.RunWithAnswer(context.Background(), tk, res.Messages, *res.Pending, bad)
	if err == nil && out.Status == "done" {
		t.Fatalf("#89 the wrong Answer key resumed silently to done — the operator's answer was discarded")
	}
	t.Logf("#89 wrong key now surfaces: err=%v status=%q", err, out.Status)

	// and the right key still works
	good := tn.Answer{ID: res.Pending.ID, Ok: true, Data: map[string]any{tn.RelayOutputKey: "staging"}}
	ok, err := c.RunWithAnswer(context.Background(), tk, res.Messages, *res.Pending, good)
	if err != nil {
		t.Fatalf("the documented key must still work: %v", err)
	}
	if ok.Status != "done" {
		t.Fatalf("resume with the right key = %q", ok.Status)
	}
}

// ---- #92 -------------------------------------------------------------------

// A timeout must report a status in the closed vocabulary, not "".
func TestIssue92_TimeoutHasAStatus(t *testing.T) {
	tk := toolkit(t)
	w := &wire{replies: []reply{{text: "too late"}}, delay: 300 * time.Millisecond}
	c := tn.CreateClient(tn.ClientOptions{
		BaseURL: "http://scripted.invalid/v1", Style: tn.StyleOpenAI, Model: "m", APIKey: "x",
		TimeoutMs: 20, Retries: 1, HTTPClient: &http.Client{Transport: w},
	})
	res, err := c.Run(context.Background(), "hi", tk)
	if err == nil {
		t.Fatal("a 20ms deadline against a 300ms wire should fail")
	}
	// There are TWO closed vocabularies both spelled `status`: the agents' set
	// (which HAS "timeout") and the client's three-value set (which does not).
	// A timed-out client run is incomplete + Limit "timeout", with the partial
	// work preserved — reading the wrong vocabulary was my own reporting bug.
	if res.Status != tn.RunStatusIncomplete {
		t.Fatalf("#92 Status = %q, want %q beside a non-nil error", res.Status, tn.RunStatusIncomplete)
	}
	if res.Limit != tn.RunLimitTimeout {
		t.Fatalf("#92 Limit = %q, want %q", res.Limit, tn.RunLimitTimeout)
	}
	for _, v := range []string{tn.RunStatusDone, tn.RunStatusPending, tn.RunStatusIncomplete} {
		if v == "timeout" {
			t.Fatal("#92 the client vocabulary must not contain \"timeout\" — that belongs to agents")
		}
	}
	t.Logf("#92 timeout => status=%q limit=%q turns=%d (partial work preserved), err=%v",
		res.Status, res.Limit, res.Turns, err)
}

// ---- #93 -------------------------------------------------------------------

// A description containing an unquoted ": " is the commonest real SKILL.md
// shape (a "Trigger on: ..." clause). Claude Code loads these; toolnexus must
// too, or a user's skill is silently invisible.
func TestIssue93_UnquotedColonInDescriptionLoads(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "timesheet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: timesheet\n" +
		"description: Work out billable hours from git commits. Trigger on: update the timesheet, do my timesheet.\n" +
		"---\n# body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	inv := tn.ListSkills(tn.LoadSkillsOptions{Dirs: []string{root}})
	if len(inv.Skills) != 1 {
		t.Fatalf("#93 the skill did not load: skills=%d skipped=%+v", len(inv.Skills), inv.Skipped)
	}
	got := inv.Skills[0]
	if got.Name != "timesheet" {
		t.Fatalf("name = %q", got.Name)
	}
	if !strings.Contains(got.Description, "Trigger on: update the timesheet") {
		t.Fatalf("#93 the description was truncated at the colon: %q", got.Description)
	}
}

// A genuinely malformed header must still be reported, with the parser's reason.
func TestIssue93_RealMalformedIsStillReported(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\ndescription: no name at all\n---\nbody"), 0o644)

	inv := tn.ListSkills(tn.LoadSkillsOptions{Dirs: []string{root}})
	if len(inv.Skills) != 0 {
		t.Fatalf("a nameless skill loaded: %+v", inv.Skills)
	}
	if len(inv.Skipped) == 0 {
		t.Fatal("the skip was not reported — a typo would vanish silently")
	}
	t.Logf("#93 skip reason = %q", inv.Skipped[0].Reason)
}

// ---- #91 -------------------------------------------------------------------

// The zero-value construction is what every first-time reader writes. This does
// not call the network; it checks the defaults point somewhere coherent.
// My original diagnosis was WRONG: jev-latest is servable on TypeSafe's own
// API. What I actually hit was following the docs to swap the base to
// OpenRouter while keeping TypeSafe's model spelling — three independent
// options that are valid in only two combinations. The fix is a preset.
func TestIssue91_BackendPresetSetsAllThreeAsAUnit(t *testing.T) {
	for _, b := range []tn.ClassifierBackend{tn.BackendTypeSafe, tn.BackendOpenRouter} {
		if _, err := tn.CreateClassifier(tn.ClassifierOptions{Backend: b}); err != nil {
			t.Fatalf("#91 preset %q must construct: %v", b, err)
		}
	}
	if _, err := tn.CreateClassifier(tn.ClassifierOptions{}); err != nil {
		t.Fatalf("zero-value construction must still work: %v", err)
	}
}

// The combination that broke me must now be refused at construction, naming
// the right spelling rather than failing later as an opaque HTTP 400.
func TestIssue91_MismatchedBaseAndModelIsRefused(t *testing.T) {
	_, err := tn.CreateClassifier(tn.ClassifierOptions{
		Style:   tn.StyleSystemOne,
		BaseURL: "https://openrouter.ai/api/v1",
		Model:   "jev-latest", // TypeSafe's spelling against OpenRouter's base
	})
	if err == nil {
		t.Fatal("#91 the base/model mismatch that cost me an afternoon still constructs silently")
	}
	if !strings.Contains(err.Error(), "typesafe/jev") {
		t.Fatalf("#91 the refusal should name the right spelling, got: %v", err)
	}
	t.Logf("#91 mismatch refused at construction: %v", err)
}
