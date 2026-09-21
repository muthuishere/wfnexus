package devinadapter_test

// Deterministic tests. No network, no real CLI: a scripted Agent stands in for
// the backend so the translation, the tool loop, skills and concurrency are
// all exercised exactly. The live tests against the real `devin` binary are in
// live_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	devinadapter "github.com/muthuishere/devinadapter"
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

func toolCall(name string, args any) string {
	return fence(map[string]any{
		"kind":       "tool_calls",
		"tool_calls": []any{map[string]any{"name": name, "arguments": args}},
	})
}

func answer(text string) string {
	return fence(map[string]any{"kind": "answer", "answer": text})
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

func newAgent(t *testing.T, back devinadapter.Agent, tweak func(*toolnexus.ClientOptions)) *toolnexus.Client {
	t.Helper()
	a := devinadapter.New(devinadapter.Options{
		Agent:   back,
		Model:   "test-model",
		Workdir: t.TempDir(),
	})
	opts := a.ClientOptions()
	if tweak != nil {
		tweak(&opts)
	}
	return toolnexus.CreateClient(opts)
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

// The second prompt must carry turn 1's tool result, or a stateless CLI would
// have no idea what it already did.
func TestTranscriptCarriesToolResult(t *testing.T) {
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

	second := back.promptAt(1)
	for _, want := range []string{"ASSISTANT CALLED: lookup(", "TOOL RESULT (lookup)", "ANSWER-42"} {
		if !strings.Contains(second, want) {
			t.Errorf("turn 2 prompt missing %q:\n%s", want, second)
		}
	}
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
		SkillsDir:  []string{"./skills"},
		ExtraTools: []toolnexus.Tool{submitTool(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := newAgent(t, back, func(o *toolnexus.ClientOptions) { o.MaxTurns = 6 }).
		Run(context.Background(), "Triage this bug.", tk)
	if err != nil {
		t.Fatal(err)
	}

	// 1. The catalog toolnexus injected into the system prompt reached the CLI.
	first := back.promptAt(0)
	if !strings.Contains(first, "bug-triage") {
		t.Errorf("skills catalog missing from the prompt file:\n%s", first)
	}
	// 2. The `skill` tool was advertised in the tool manifest.
	if !strings.Contains(first, `"name":"skill"`) && !strings.Contains(first, `"name": "skill"`) {
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

func TestParseToolReply(t *testing.T) {
	cases := []struct {
		name, in, wantFinish, wantContent string
		wantCalls                         int
	}{
		{name: "fenced answer", in: answer("hello"), wantFinish: "stop", wantContent: "hello"},
		{name: "bare json answer", in: `{"kind":"answer","answer":"hi"}`, wantFinish: "stop", wantContent: "hi"},
		{name: "prose passthrough", in: "  just prose  ", wantFinish: "stop", wantContent: "just prose"},
		{name: "tool call", in: toolCall("t", map[string]any{"a": 1}), wantFinish: "tool_calls", wantCalls: 1},
		{name: "nested braces survive", in: toolCall("t", map[string]any{"o": map[string]any{"k": "v"}}), wantFinish: "tool_calls", wantCalls: 1},
		// A live devin run produced exactly this: prose answer AND a call.
		{name: "answer with tool_calls still calls", in: fence(map[string]any{
			"kind": "answer", "answer": "all done",
			"tool_calls": []any{map[string]any{"name": "submit_answer", "arguments": map[string]any{"a": 1}}},
		}), wantFinish: "tool_calls", wantCalls: 1},
		// Another live drift: the payload placed in `answer` as an object.
		{name: "object answer survives", in: fence(map[string]any{
			"kind": "answer", "answer": map[string]any{"severity": "high"},
		}), wantFinish: "stop", wantContent: `{"severity":"high"}`},
		{name: "malformed json degrades", in: "```json\n{oops}\n```", wantFinish: "stop"},
		{name: "empty tool_calls degrades", in: fence(map[string]any{"kind": "tool_calls", "tool_calls": []any{}}), wantFinish: "stop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, finish := devinadapter.ParseToolReply(c.in)
			if finish != c.wantFinish {
				t.Fatalf("finish = %q, want %q", finish, c.wantFinish)
			}
			if c.wantCalls > 0 {
				calls, _ := msg["tool_calls"].([]any)
				if len(calls) != c.wantCalls {
					t.Fatalf("calls = %d, want %d", len(calls), c.wantCalls)
				}
				return
			}
			if c.wantContent != "" && msg["content"] != c.wantContent {
				t.Fatalf("content = %v, want %q", msg["content"], c.wantContent)
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
			[]string{"--prompt-file", devinadapter.PlaceholderFile, "-p", "--permission-mode", "auto", "--model", "opus"}},
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

// One adapter, several toolnexus clients, concurrent runs. Run with -race.
func TestOneAdapterManyClientsConcurrently(t *testing.T) {
	back := devinadapter.AgentFunc{Label: "echo", Fn: func(_ context.Context, turn devinadapter.Turn) (string, error) {
		// Answer with whatever the user asked, so each client can prove it got
		// its OWN reply and not another's.
		for _, line := range strings.Split(turn.Prompt, "\n") {
			if strings.HasPrefix(line, "ping-") {
				return answer(line), nil
			}
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
			opts := a.ClientOptions()
			opts.SystemPrompt = fmt.Sprintf("client %d", i)
			res, err := toolnexus.CreateClient(opts).
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

// --- the HTTP seam on its own --------------------------------------------

// The adapter is a plain RoundTripper, so it works with any OpenAI-style
// client, not just toolnexus.
func TestAdapterIsAUsableRoundTripperAlone(t *testing.T) {
	a := devinadapter.New(devinadapter.Options{
		Agent:   devinadapter.AgentFunc{Fn: func(context.Context, devinadapter.Turn) (string, error) { return answer("pong"), nil }},
		Model:   "my-model",
		Workdir: t.TempDir(),
	})

	body := `{"model":"ignored","messages":[{"role":"user","content":"ping"}]}`
	resp, err := a.HTTPClient().Post(devinadapter.DefaultBaseURL+"/chat/completions",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Model != "my-model" {
		t.Errorf("model = %q", out.Model)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "pong" || out.Choices[0].FinishReason != "stop" {
		t.Errorf("unexpected completion: %+v", out.Choices)
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
			if _, err := toolnexus.CreateClient(a.ClientOptions()).Run(context.Background(), "hi", tk); err != nil {
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
	_, err := toolnexus.CreateClient(a.ClientOptions()).Run(context.Background(), "hi", tk)
	if err == nil {
		t.Fatal("expected the backend error to surface")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("error lost its cause: %v", err)
	}
}
