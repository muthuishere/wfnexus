//go:build toolnexus_inprocess

// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag transport.go breaks `go build ./...` for the whole
// module, and the package is tagged as a unit so its tests keep compiling.
// Nothing is deleted:
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// Drop the tag once a toolnexus release carries the export.

package devinadapter_test

// Deterministic tests. No network, no real CLI: a scripted Agent stands in for
// the backend so the translation, the tool loop, skills and concurrency are
// all exercised exactly. The live tests against the real `devin` binary are in
// live_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	devinadapter "github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
	toolnexus "github.com/muthuishere/toolnexus/golang"
)

// Triage is the structural answer — its json tags ARE the schema toolnexus
// advertises to the model.
type Triage struct {
	Summary    string   `json:"summary"`
	RootCause  string   `json:"root_cause"`
	Severity   string   `json:"severity"`
	Files      []string `json:"files"`
	Fix        []string `json:"fix"`
	Confidence float64  `json:"confidence"`
	Notes      string   `json:"notes,omitempty"`
}

// scripted is an Agent that replays canned replies and records every prompt it
// was given, so a test can assert on what the adapter actually rendered.
type scripted struct {
	mu      sync.Mutex
	replies []string
	prompts []string
	files   []string
}

func (s *scripted) Name() string { return "scripted" }

func (s *scripted) Execute(_ context.Context, t devinadapter.Turn) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, t.Prompt)
	s.files = append(s.files, t.PromptFile)

	// The prompt must be on disk when the Agent runs — that is the contract a
	// real CLI depends on.
	if b, err := os.ReadFile(t.PromptFile); err != nil {
		return "", fmt.Errorf("prompt file unreadable: %w", err)
	} else if string(b) != t.Prompt {
		return "", fmt.Errorf("prompt file does not match Turn.Prompt")
	}

	i := len(s.prompts) - 1
	if i >= len(s.replies) {
		return "", fmt.Errorf("scripted: no reply for turn %d", t.Index)
	}
	return s.replies[i], nil
}

func (s *scripted) promptAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.prompts) {
		return ""
	}
	return s.prompts[i]
}

func fence(v any) string {
	b, _ := json.Marshal(v)
	return "Here you go:\n\n```json\n" + string(b) + "\n```\n"
}

// toolCall / answer emit what the contract now asks for: an OpenAI assistant
// message in a fence.
func toolCall(name string, args any) string {
	return fence(map[string]any{
		"role":    "assistant",
		"content": nil,
		"tool_calls": []any{map[string]any{
			"type":     "function",
			"function": map[string]any{"name": name, "arguments": args},
		}},
	})
}

func answer(text string) string {
	return fence(map[string]any{"role": "assistant", "content": text, "tool_calls": []any{}})
}

// submitTool is the structured-output tool: the run cannot finish without the
// model filling Triage's schema.
func submitTool(into **Triage) toolnexus.Tool {
	return toolnexus.NativeToolReflect[Triage](
		"submit_answer",
		"Submit the final structured triage. Call exactly once, last.",
		func(_ context.Context, in Triage) (string, error) {
			captured := in
			*into = &captured
			return "recorded", nil
		},
	)
}

func newAgent(t *testing.T, back devinadapter.Agent, tweak func(*toolnexus.InProcessOptions)) *toolnexus.Client {
	t.Helper()
	a := devinadapter.New(devinadapter.Options{
		Agent:   back,
		Model:   "test-model",
		Workdir: t.TempDir(),
	})
	opts := a.InProcessOptions()
	if tweak != nil {
		tweak(&opts)
	}
	return toolnexus.CreateInProcessClient(opts)
}

// --- the structural round trip ------------------------------------------

func TestStructuredAnswerThroughToolLoop(t *testing.T) {
	want := Triage{
		Summary:    "coupon applies twice",
		RootCause:  "applyCoupon appends without a dedupe check",
		Severity:   "high",
		Files:      []string{"apps/api/internal/engine/pricing.go"},
		Fix:        []string{"reject a coupon code already in order.Discounts"},
		Confidence: 0.92,
	}
	back := &scripted{replies: []string{
		toolCall("submit_answer", want),
		answer("Filed."),
	}}

	var got *Triage
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins:   false,
		ExtraTools: []toolnexus.Tool{submitTool(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := newAgent(t, back, nil).Run(context.Background(), "Triage this bug.", tk)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatalf("no structured answer; status=%s text=%q", res.Status, res.Text)
	}
	if got.Severity != want.Severity || got.Confidence != want.Confidence {
		t.Errorf("schema not round-tripped: got %+v", *got)
	}
	if len(got.Files) != 1 || got.Files[0] != want.Files[0] {
		t.Errorf("slice field lost: %+v", got.Files)
	}
	if res.Status != "done" || res.ToolCallCount != 1 || res.Turns != 2 {
		t.Errorf("loop shape: status=%s calls=%d turns=%d", res.Status, res.ToolCallCount, res.Turns)
	}
	if res.Text != "Filed." {
		t.Errorf("final text = %q", res.Text)
	}
}

// Every request reaching the CLI is the verbatim OpenAI body in an envelope,
// so turn 2 carries turn 1's call and its result with nothing re-rendered.
func TestRequestReachesTheCLIVerbatim(t *testing.T) {
	back := &scripted{replies: []string{
		toolCall("lookup", map[string]any{"id": 7}),
		answer("done"),
	}}
	lookup := toolnexus.NativeTool("lookup", "look something up", nil,
		func(context.Context, map[string]any) (string, error) { return "ANSWER-42", nil })

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false, ExtraTools: []toolnexus.Tool{lookup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAgent(t, back, nil).Run(context.Background(), "go", tk); err != nil {
		t.Fatal(err)
	}

	first := back.promptAt(0)
	if !strings.Contains(first, `<openai_request endpoint="/v1/chat/completions">`) {
		t.Errorf("request envelope missing:\n%s", first)
	}
	if !strings.Contains(first, "<openai_response>") {
		t.Errorf("response contract missing:\n%s", first)
	}
	// The tools array must arrive as the real OpenAI schema, not a summary.
	if !strings.Contains(first, `"name": "lookup"`) || !strings.Contains(first, `"parameters"`) {
		t.Errorf("tool schema not passed through:\n%s", first)
	}

	// Turn 2: the assistant's call and the tool result, in OpenAI shape.
	second := back.promptAt(1)
	body := extractRequest(t, second)
	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("envelope does not hold valid json: %v", err)
	}

	var callID, resultFor string
	var result any
	for _, m := range req.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 {
			callID = m.ToolCalls[0].ID
			if m.ToolCalls[0].Function.Name != "lookup" {
				t.Errorf("call name = %q", m.ToolCalls[0].Function.Name)
			}
			if m.ToolCalls[0].Function.Arguments != `{"id":7}` {
				t.Errorf("arguments = %q", m.ToolCalls[0].Function.Arguments)
			}
		}
		if m.Role == "tool" {
			resultFor, result = m.ToolCallID, m.Content
		}
	}
	if callID == "" || resultFor != callID {
		t.Errorf("tool result not linked to the call: call=%q result_for=%q", callID, resultFor)
	}
	if result != "ANSWER-42" {
		t.Errorf("tool output = %v, want ANSWER-42", result)
	}
}

// extractRequest pulls the JSON body back out of the envelope.
func extractRequest(t *testing.T, prompt string) []byte {
	t.Helper()
	const open = `<openai_request endpoint="/v1/chat/completions">`
	i := strings.Index(prompt, open)
	j := strings.Index(prompt, "</openai_request>")
	if i < 0 || j < 0 {
		t.Fatalf("no request envelope in:\n%s", prompt)
	}
	return []byte(strings.TrimSpace(prompt[i+len(open) : j]))
}

// --- skills --------------------------------------------------------------

func TestSkillsReachTheCLIAndCanBeInvoked(t *testing.T) {
	back := &scripted{replies: []string{
		toolCall("skill", map[string]any{"name": "bug-triage"}),
		toolCall("submit_answer", Triage{
			Summary: "s", RootCause: "r", Severity: "high",
			Files: []string{"f.go"}, Fix: []string{"x"}, Confidence: 0.9,
		}),
		answer("Filed."),
	}}

	var got *Triage
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins:   false,
		SkillsDir:  []string{"./testdata/skills"},
		ExtraTools: []toolnexus.Tool{submitTool(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := newAgent(t, back, func(o *toolnexus.InProcessOptions) { o.MaxTurns = 6 }).
		Run(context.Background(), "Triage this bug.", tk)
	if err != nil {
		t.Fatal(err)
	}

	// 1. The catalog toolnexus injected into the system message reached the CLI.
	first := back.promptAt(0)
	if !strings.Contains(first, "bug-triage") {
		t.Errorf("skills catalog missing from the request:\n%s", first)
	}
	// 2. The `skill` tool was advertised in the request's tools array.
	if !strings.Contains(first, `"name": "skill"`) {
		t.Errorf("skill tool not advertised:\n%s", first)
	}
	// 3. Invoking it fed the skill BODY back on the next turn.
	second := back.promptAt(1)
	if !strings.Contains(second, "Severity ladder") || !strings.Contains(second, "never") {
		t.Errorf("skill body not returned to the model:\n%s", second)
	}
	if got == nil {
		t.Fatalf("no structured answer; status=%s", res.Status)
	}
	if res.ToolCallCount != 2 {
		t.Errorf("expected skill + submit_answer, got %d calls", res.ToolCallCount)
	}
}

// --- reply parsing -------------------------------------------------------

func TestParseReply(t *testing.T) {
	cases := []struct {
		name, in, wantFinish, wantContent string
		wantCalls                         int
		wantErr                           bool
	}{
		{name: "assistant message", in: answer("hello"), wantFinish: "stop", wantContent: "hello"},
		{name: "tool call", in: toolCall("t", map[string]any{"a": 1}), wantFinish: "tool_calls", wantCalls: 1},
		{name: "nested braces survive", in: toolCall("t", map[string]any{"o": map[string]any{"k": "v"}}), wantFinish: "tool_calls", wantCalls: 1},
		{name: "unfenced object", in: `{"role":"assistant","content":"hi"}`, wantFinish: "stop", wantContent: "hi"},
		{name: "whole chat.completion unwraps", in: fence(map[string]any{
			"object":  "chat.completion",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "unwrapped"}}},
		}), wantFinish: "stop", wantContent: "unwrapped"},
		{name: "flat call shape", in: fence(map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{"name": "t", "args": map[string]any{"x": 1}}},
		}), wantFinish: "tool_calls", wantCalls: 1},
		// Compatibility with routsi's envelope.
		{name: "legacy answer envelope", in: fence(map[string]any{"kind": "answer", "answer": "hi"}), wantFinish: "stop", wantContent: "hi"},
		{name: "legacy tool envelope", in: fence(map[string]any{
			"kind": "tool_calls", "tool_calls": []any{map[string]any{"name": "t", "arguments": map[string]any{}}},
		}), wantFinish: "tool_calls", wantCalls: 1},
		// A live devin run produced this: prose answer AND a call in one object.
		{name: "content plus tool_calls calls", in: fence(map[string]any{
			"role": "assistant", "content": "all done",
			"tool_calls": []any{map[string]any{"function": map[string]any{"name": "submit_answer", "arguments": map[string]any{"a": 1}}}},
		}), wantFinish: "tool_calls", wantCalls: 1},

		// Everything below must ERROR so the turn is retried, never guessed at.
		{name: "prose is not a reply", in: "just prose", wantErr: true},
		{name: "malformed json", in: "```json\n{oops}\n```", wantErr: true},
		{name: "empty object", in: fence(map[string]any{}), wantErr: true},
		{name: "content null with no calls", in: fence(map[string]any{"role": "assistant", "content": nil}), wantErr: true},
		{name: "empty content", in: fence(map[string]any{"role": "assistant", "content": "  "}), wantErr: true},
		// Another live drift: the payload placed in content as an object.
		{name: "structured content rejected", in: fence(map[string]any{
			"role": "assistant", "content": map[string]any{"severity": "high"},
		}), wantErr: true},
		{name: "call without a name", in: fence(map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{"arguments": map[string]any{}}},
		}), wantErr: true},
		{name: "tool_calls not an array", in: fence(map[string]any{
			"role": "assistant", "tool_calls": "submit_answer",
		}), wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := devinadapter.ParseReply(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", res)
				}
				if !errors.Is(err, devinadapter.ErrUnparseable) {
					t.Fatalf("error should wrap ErrUnparseable: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gotFinish := "stop"
			if len(res.ToolCalls) > 0 {
				gotFinish = "tool_calls"
			}
			if gotFinish != c.wantFinish {
				t.Fatalf("finish = %q, want %q", gotFinish, c.wantFinish)
			}
			if c.wantCalls > 0 {
				if len(res.ToolCalls) != c.wantCalls {
					t.Fatalf("calls = %d, want %d", len(res.ToolCalls), c.wantCalls)
				}
				if res.ToolCalls[0].Name == "" {
					t.Fatal("call has no name")
				}
				return
			}
			if res.Content != c.wantContent {
				t.Fatalf("content = %q, want %q", res.Content, c.wantContent)
			}
		})
	}
}

// A tool call whose arguments arrived as a JSON-encoded string must still
// decode into the schema — models do this often enough to matter.
func TestStringEncodedArgumentsDecode(t *testing.T) {
	raw, _ := json.Marshal(Triage{
		Summary: "s", RootCause: "r", Severity: "low",
		Files: []string{"a.go"}, Fix: []string{"b"}, Confidence: 0.5,
	})
	back := &scripted{replies: []string{
		fence(map[string]any{
			"kind":       "tool_calls",
			"tool_calls": []any{map[string]any{"name": "submit_answer", "arguments": string(raw)}},
		}),
		answer("ok"),
	}}

	var got *Triage
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false, ExtraTools: []toolnexus.Tool{submitTool(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAgent(t, back, nil).Run(context.Background(), "go", tk); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Severity != "low" {
		t.Fatalf("string-encoded args not decoded: %+v", got)
	}
}

// --- the command seam ----------------------------------------------------

// CommandAgent drives a real process. A shell script stands in for the CLI so
// the argv template, the prompt file and stdout capture are all genuinely
// exercised.
func TestCommandAgentRunsARealProcess(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-cli")
	// Echoes a marker, the flag it was given, and the prompt file's contents.
	body := "#!/bin/sh\necho \"MODE=$1 FILE=$2\"\ncat \"$2\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	agent := &devinadapter.CommandAgent{
		Label: "fake",
		Bin:   script,
		Args:  []string{"--prompt-file", devinadapter.PlaceholderFile},
	}
	if agent.Name() != "fake" {
		t.Errorf("Name() = %q", agent.Name())
	}

	f := filepath.Join(dir, "p.md")
	if err := os.WriteFile(f, []byte("PROMPT BODY"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := agent.Execute(context.Background(), devinadapter.Turn{
		Index: 1, PromptFile: f, Prompt: "PROMPT BODY", Workdir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "MODE=--prompt-file") || !strings.Contains(out, "PROMPT BODY") {
		t.Fatalf("template or file substitution wrong: %q", out)
	}
}

func TestCommandAgentReportsFailureWithStderr(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "boom")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'not logged in' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	agent := &devinadapter.CommandAgent{Label: "boom", Bin: script}
	_, err := agent.Execute(context.Background(), devinadapter.Turn{Workdir: dir, PromptFile: script})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "not logged in") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should name the backend and carry stderr: %v", err)
	}
}

// The presets differ only in argv, which is the point of the interface.
func TestPresetsBuildTheRightArgv(t *testing.T) {
	for _, c := range []struct {
		name  string
		agent *devinadapter.CommandAgent
		want  []string
	}{
		{"devin", devinadapter.Devin(devinadapter.CLI{Model: "opus"}),
			[]string{"--prompt-file", devinadapter.PlaceholderFile, "-p", "--permission-mode", devinadapter.PermissionBypass, "--model", "opus"}},
		{"claude", devinadapter.Claude(devinadapter.CLI{}),
			[]string{"-p", devinadapter.PlaceholderPrompt}},
		{"copilot", devinadapter.Copilot(devinadapter.CLI{}),
			[]string{"-p", devinadapter.PlaceholderPrompt, "--log-level", "none", "--no-color"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.agent.Bin != c.name {
				t.Errorf("Bin = %q, want %q", c.agent.Bin, c.name)
			}
			if strings.Join(c.agent.Args, " ") != strings.Join(c.want, " ") {
				t.Errorf("args = %v, want %v", c.agent.Args, c.want)
			}
		})
	}
}

// --- multiple clients ----------------------------------------------------

var pingMarker = regexp.MustCompile(`ping-\d+`)

// One adapter, several toolnexus clients, concurrent runs. Run with -race.
func TestOneAdapterManyClientsConcurrently(t *testing.T) {
	back := devinadapter.AgentFunc{Label: "echo", Fn: func(_ context.Context, turn devinadapter.Turn) (string, error) {
		// Echo the marker from the request body, so each client can prove it
		// got its OWN reply and not another's.
		if m := pingMarker.FindString(turn.Prompt); m != "" {
			return answer(m), nil
		}
		return answer("no marker"), nil
	}}

	var traced int64
	var mu sync.Mutex
	a := devinadapter.New(devinadapter.Options{
		Agent:   back,
		Workdir: t.TempDir(),
		Trace:   func(devinadapter.Exchange) { mu.Lock(); traced++; mu.Unlock() },
	})

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	texts := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A separate client per goroutine, all sharing one Adapter.
			opts := a.InProcessOptions()
			opts.SystemPrompt = fmt.Sprintf("client %d", i)
			res, err := toolnexus.CreateInProcessClient(opts).
				Run(context.Background(), fmt.Sprintf("ping-%d", i), tk)
			errs[i], texts[i] = err, res.Text
		}()
	}
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Fatalf("client %d: %v", i, errs[i])
		}
		if want := fmt.Sprintf("ping-%d", i); texts[i] != want {
			t.Errorf("client %d got %q, want %q — replies crossed", i, texts[i], want)
		}
	}
	if traced != n {
		t.Errorf("traced %d exchanges, want %d", traced, n)
	}
}

// --- Generate on its own -------------------------------------------------

// Generate is usable without a client: one assembled request in, one assistant
// message out.
func TestGenerateStandsAlone(t *testing.T) {
	a := devinadapter.New(devinadapter.Options{
		Agent:   devinadapter.AgentFunc{Fn: func(context.Context, devinadapter.Turn) (string, error) { return answer("pong"), nil }},
		Workdir: t.TempDir(),
	})

	res, err := a.Generate(toolnexus.InProcessRequest{
		Model: "my-model",
		Body: map[string]any{
			"model":    "my-model",
			"messages": []any{map[string]any{"role": "user", "content": "ping"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "pong" || len(res.ToolCalls) != 0 {
		t.Errorf("unexpected response: %+v", res)
	}
}

// Prompt files are cleaned up by default and kept on request.
func TestPromptFileLifecycle(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%v", keep), func(t *testing.T) {
			dir := t.TempDir()
			var seen string
			a := devinadapter.New(devinadapter.Options{
				Agent: devinadapter.AgentFunc{Fn: func(_ context.Context, turn devinadapter.Turn) (string, error) {
					seen = turn.PromptFile
					return answer("ok"), nil
				}},
				Workdir:         dir,
				KeepPromptFiles: keep,
			})
			tk, _ := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
			if _, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).Run(context.Background(), "hi", tk); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(seen)
			if keep && err != nil {
				t.Errorf("prompt file should have been kept: %v", err)
			}
			if !keep && err == nil {
				t.Errorf("prompt file should have been removed: %s", seen)
			}
		})
	}
}

// A backend failure must surface, not be silently retried into a wrong answer.
func TestBackendErrorSurfaces(t *testing.T) {
	a := devinadapter.New(devinadapter.Options{
		Agent: devinadapter.AgentFunc{Label: "broken", Fn: func(context.Context, devinadapter.Turn) (string, error) {
			return "", fmt.Errorf("devin: not authenticated")
		}},
		Workdir: t.TempDir(),
	})
	tk, _ := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
	_, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).Run(context.Background(), "hi", tk)
	if err == nil {
		t.Fatal("expected the backend error to surface")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("error lost its cause: %v", err)
	}
}

// --- validation, repair and failure --------------------------------------

// A bad reply is handed back with the complaint, and the corrected reply is
// used. The run must not see the failure at all.
func TestBadReplyIsRepairedAndRetried(t *testing.T) {
	var prompts []string
	back := devinadapter.AgentFunc{Label: "flaky", Fn: func(_ context.Context, turn devinadapter.Turn) (string, error) {
		prompts = append(prompts, turn.Prompt)
		if turn.Attempt == 1 {
			// The exact live drift: the payload placed in content as an object.
			return fence(map[string]any{
				"role": "assistant", "content": map[string]any{"severity": "high"},
			}), nil
		}
		return answer("repaired"), nil
	}}

	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	tk, _ := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
	res, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).Run(context.Background(), "hi", tk)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "repaired" {
		t.Errorf("text = %q, want the repaired reply", res.Text)
	}
	if len(prompts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(prompts))
	}
	// The repair prompt must carry both the offending output and the reason.
	if !strings.Contains(prompts[1], "could not be parsed") {
		t.Errorf("repair prompt lacks the complaint:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[1], "structured data belongs in a tool call") {
		t.Errorf("repair prompt lacks the specific cause:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[1], "severity") {
		t.Errorf("repair prompt lacks the bad output:\n%s", prompts[1])
	}
}

// When the backend never produces a valid reply, the run FAILS. It must never
// fall back to passing prose off as an answer.
func TestExhaustedRepairsFailsTheRun(t *testing.T) {
	var attempts int
	back := devinadapter.AgentFunc{Label: "stubborn", Fn: func(context.Context, devinadapter.Turn) (string, error) {
		attempts++
		return "I refuse to emit json.", nil
	}}

	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir(), Repairs: 2})
	tk, _ := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
	_, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).Run(context.Background(), "hi", tk)
	if err == nil {
		t.Fatal("expected the run to fail rather than accept prose")
	}
	if attempts != 3 { // the turn plus 2 repairs
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if !strings.Contains(err.Error(), "stubborn") || !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("error should name the backend and the budget: %v", err)
	}
}

// Repairs can be switched off entirely, for a caller who would rather fail fast.
func TestRepairsCanBeDisabled(t *testing.T) {
	var attempts int
	back := devinadapter.AgentFunc{Fn: func(context.Context, devinadapter.Turn) (string, error) {
		attempts++
		return "nope", nil
	}}
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir(), Repairs: -1})
	tk, _ := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
	if _, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).Run(context.Background(), "hi", tk); err == nil {
		t.Fatal("expected failure")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

// --- model routing -------------------------------------------------------

// toolnexus passes the model name down; the adapter decides what the CLI gets.
func TestModelNameRouting(t *testing.T) {
	cases := []struct {
		name        string
		optsModel   string
		clientModel string // "" ⇒ leave ClientOptions as built
		wantTurn    string // what the Agent should see
		wantReport  string // what RunResult.Model shows (toolnexus's own view)
	}{
		{name: "nothing chosen", wantTurn: "", wantReport: devinadapter.DefaultModelLabel},
		{name: "client chooses", clientModel: "claude-opus-4.6", wantTurn: "claude-opus-4.6", wantReport: "claude-opus-4.6"},
		// A pinned adapter overrides what the client asked for, downstream.
		// RunResult.Model still reports the client's own setting — that field
		// is toolnexus echoing its config, not the completion.
		{name: "adapter pins", optsModel: "codex", clientModel: "ignored", wantTurn: "codex", wantReport: "ignored"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var seen string
			a := devinadapter.New(devinadapter.Options{
				Model:   c.optsModel,
				Workdir: t.TempDir(),
				Agent: devinadapter.AgentFunc{Fn: func(_ context.Context, turn devinadapter.Turn) (string, error) {
					seen = turn.Model
					return answer("ok"), nil
				}},
			})
			opts := a.InProcessOptions()
			if c.clientModel != "" {
				opts.Model = c.clientModel
			}
			tk, _ := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
			res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), "hi", tk)
			if err != nil {
				t.Fatal(err)
			}
			if seen != c.wantTurn {
				t.Errorf("Agent saw model %q, want %q", seen, c.wantTurn)
			}
			if res.Model != c.wantReport {
				t.Errorf("completion reported %q, want %q", res.Model, c.wantReport)
			}
		})
	}
}

// The model reaches the CLI's argv only when nothing pinned it.
func TestCommandAgentModelFlag(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "show-args")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "p.md")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(agent *devinadapter.CommandAgent, model string) string {
		agent.Bin = script
		out, err := agent.Execute(context.Background(), devinadapter.Turn{
			Index: 1, PromptFile: f, Prompt: "x", Model: model, Workdir: dir,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := run(devinadapter.Devin(devinadapter.CLI{}), "opus"); !strings.Contains(got, "--model opus") {
		t.Errorf("per-turn model not passed: %q", got)
	}
	if got := run(devinadapter.Devin(devinadapter.CLI{}), ""); strings.Contains(got, "--model") {
		t.Errorf("empty model should not produce a flag: %q", got)
	}
	// A pinned preset ignores the per-turn model instead of passing both.
	got := run(devinadapter.Devin(devinadapter.CLI{Model: "pinned"}), "opus")
	if !strings.Contains(got, "--model pinned") || strings.Contains(got, "opus") {
		t.Errorf("pinned model should win alone: %q", got)
	}
}
