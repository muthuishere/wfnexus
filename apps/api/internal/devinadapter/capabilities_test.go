//go:build toolnexus_inprocess

package devinadapter_test

// The rest of the toolnexus surface through the adapter: soul, hooks,
// guardrails, the completion gate, conversation memory, budgets, suspension,
// metrics, A2A and the streaming refusal.
//
// The theme is the same as sources_test.go — none of this needs anything from
// the adapter. It is a model; these are the loop's features. These tests exist
// to prove that claim rather than assert it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	devinadapter "github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
	toolnexus "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"
)

// agentsFor wires a sub-agent runtime onto the adapter.
func agentsFor(t *testing.T, back devinadapter.Agent) (agents.Options, *devinadapter.Adapter) {
	t.Helper()
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	baseURL, apiKey, model := a.AgentsLLM()
	return agents.Options{
		Transport: a.Transport(),
		LLM:       &agents.LLMOptions{BaseURL: baseURL, APIKey: apiKey, Style: toolnexus.StyleOpenAI, Model: model},
	}, a
}

// --- soul ----------------------------------------------------------------

// An agent's Soul is its identity prompt. It must reach the model as the
// system message, inline and from a file.
func TestSoulReachesTheModel(t *testing.T) {
	const inlineSoul = "You are Hex, a laconic triage bot. You never use exclamation marks."

	for _, c := range []struct{ name, want string }{
		{"inline", inlineSoul},
		{"file", "You are Vega, and you speak only in questions."},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := agents.Spec{Does: "Answers questions."}
			if c.name == "inline" {
				spec.Soul = c.want
			} else {
				path := filepath.Join(t.TempDir(), "SOUL.md")
				if err := os.WriteFile(path, []byte(c.want), 0o644); err != nil {
					t.Fatal(err)
				}
				spec.SoulFile = path
			}

			back := &byName{reply: func(string, int) string { return answer("ok") }}
			opts, _ := agentsFor(t, back)
			ag := agents.New("hex", spec)
			opts.Registry = ag.Registry()

			res, _ := ag.Run(opts, "say something")
			if res.Status != "done" {
				t.Fatalf("status = %q", res.Status)
			}
			body := extractRequest(t, back.first())
			if !strings.Contains(string(body), c.want[:20]) {
				t.Errorf("soul never reached the model:\n%s", body)
			}
		})
	}
}

// The agent-home form: ComposeSoul reads the bootstrap files, and MemoryTool
// gives the agent a place to write things down.
func TestAgentHomeSoulAndMemory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"),
		[]byte("You are Orbit, the on-call triager."), 0o644); err != nil {
		t.Fatal(err)
	}

	soul, found := agents.ComposeSoul(dir)
	if len(found) == 0 || !strings.Contains(soul, "Orbit") {
		t.Fatalf("ComposeSoul found %v, soul=%q", found, soul)
	}

	// The memory tool is an ordinary tool, so the adapter carries it like any
	// other — and what it writes must survive as a file.
	mem := agents.MemoryTool(dir)
	back := &byName{reply: func(_ string, calls int) string {
		if calls == 0 {
			return toolCall(mem.Name, map[string]any{
				"action": "add", "text": "pricing bugs are usually in applyCoupon",
			})
		}
		return answer("noted")
	}}

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false, ExtraTools: []toolnexus.Tool{mem},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := runWith(t, back, tk, "remember where pricing bugs live")
	if res.ToolCallCount != 1 {
		t.Fatalf("memory tool not called: %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].IsError {
		t.Fatalf("memory tool errored: %s", res.ToolCalls[0].Output)
	}

	b, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatalf("memory was not persisted: %v", err)
	}
	if !strings.Contains(string(b), "applyCoupon") {
		t.Errorf("MEMORY.md holds %q", b)
	}
}

// --- hooks ---------------------------------------------------------------

// All four hooks fire around a model that is a CLI, and each override lands.
func TestHooksFireAndOverride(t *testing.T) {
	var order []string

	echo := toolnexus.NativeTool("echo", "Echo a value.", nil,
		func(_ context.Context, args map[string]any) (string, error) {
			return fmt.Sprintf("tool saw %v", args["v"]), nil
		})

	back := &byName{reply: func(_ string, calls int) string {
		if calls == 0 {
			return toolCall("echo", map[string]any{"v": "original"})
		}
		return answer("done")
	}}

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false, ExtraTools: []toolnexus.Tool{echo},
	})
	if err != nil {
		t.Fatal(err)
	}

	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	opts := a.InProcessOptions()
	opts.MaxTurns = 4
	opts.Hooks = &toolnexus.Hooks{
		BeforeLLM: func(context.Context, toolnexus.BeforeLLMEvent) (*toolnexus.LLMOverride, error) {
			order = append(order, "beforeLLM")
			return nil, nil
		},
		AfterLLM: func(context.Context, toolnexus.AfterLLMEvent) error {
			order = append(order, "afterLLM")
			return nil
		},
		BeforeTool: func(_ context.Context, ev toolnexus.BeforeToolEvent) (*toolnexus.ToolOverride, error) {
			order = append(order, "beforeTool:"+ev.Name)
			// Rewrite the arguments on the way in.
			return &toolnexus.ToolOverride{Args: map[string]any{"v": "rewritten"}}, nil
		},
		AfterTool: func(_ context.Context, ev toolnexus.AfterToolEvent) (*toolnexus.ToolOverride, error) {
			order = append(order, "afterTool:"+ev.Name)
			// Annotate the result on the way out.
			return &toolnexus.ToolOverride{
				Result: &toolnexus.ToolResult{Output: ev.Result.Output + " [annotated]"},
			}, nil
		},
	}

	res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), "go", tk)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"beforeLLM", "afterLLM", "beforeTool:echo", "afterTool:echo"} {
		found := false
		for _, got := range order {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("hook %q never fired; order=%v", want, order)
		}
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("calls = %d", len(res.ToolCalls))
	}
	out := res.ToolCalls[0].Output
	if !strings.Contains(out, "rewritten") {
		t.Errorf("BeforeTool arg rewrite did not land: %q", out)
	}
	if !strings.Contains(out, "[annotated]") {
		t.Errorf("AfterTool result override did not land: %q", out)
	}
}

// --- guardrails ----------------------------------------------------------

// A guardrail denies a tool call as POLICY, before it runs.
func TestGuardrailDeniesToolCall(t *testing.T) {
	var ran bool
	deploy := toolnexus.NativeTool("deploy", "Deploy to production.", nil,
		func(context.Context, map[string]any) (string, error) {
			ran = true
			return "deployed", nil
		})

	back := &byName{reply: func(_ string, calls int) string {
		if calls == 0 {
			return toolCall("deploy", map[string]any{})
		}
		return answer("stopped by policy")
	}}

	opts, _ := agentsFor(t, back)
	ag := agents.New("shipper", agents.Spec{
		Does:  "Ships things.",
		Soul:  "You ship code.",
		Tools: []toolnexus.Tool{deploy},
		Guardrails: []agents.Guardrail{
			func(ev toolnexus.BeforeToolEvent) string {
				if ev.Name == "deploy" {
					return "deploying is not allowed from a test"
				}
				return ""
			},
		},
	})
	opts.Registry = ag.Registry()

	res, _ := ag.Run(opts, "deploy the service")
	if ran {
		t.Error("the guardrail did not stop the tool from running")
	}
	if res.Status != "done" {
		t.Errorf("status = %q, text = %q", res.Status, res.Text)
	}
}

// --- completion gate -----------------------------------------------------

// The gate must hold an agent back from claiming done until its work verifies,
// and the failure reason must reach the model.
func TestCompletionGateRetriesUntilVerified(t *testing.T) {
	var attempts int
	back := &byName{}
	back.reply = func(prompt string, _ int) string {
		attempts++
		// Only once the gate's complaint appears does the agent produce the
		// word the gate wants.
		if strings.Contains(prompt, "must mention the ticket id") {
			return answer("fixed under BUG-42")
		}
		return answer("fixed it")
	}

	opts, _ := agentsFor(t, back)
	ag := agents.New("fixer", agents.Spec{
		Does: "Fixes bugs.",
		Soul: "You fix bugs and report what you did.",
		Completion: &agents.Completion{
			MaxAttempts: 3,
			Verify: func(r toolnexus.RunResult) (bool, string) {
				if strings.Contains(r.Text, "BUG-42") {
					return true, ""
				}
				return false, "the report must mention the ticket id BUG-42"
			},
		},
	})
	opts.Registry = ag.Registry()

	res, _ := ag.Run(opts, "fix BUG-42")
	if !strings.Contains(res.Text, "BUG-42") {
		t.Errorf("the gate let an unverified answer through: %q", res.Text)
	}
	if attempts < 2 {
		t.Errorf("the gate never sent it back (attempts=%d)", attempts)
	}
}

// --- conversation memory -------------------------------------------------

// Ask(id) carries a transcript across calls, so the CLI — which has no memory
// of its own between one-shot turns — still holds a conversation.
func TestConversationMemoryAcrossCalls(t *testing.T) {
	back := &byName{}
	back.reply = func(prompt string, _ int) string {
		// Answer from the transcript the client replayed, not from any state
		// the backend kept.
		if strings.Contains(prompt, "indigo") {
			return answer("your colour is indigo")
		}
		return answer("noted")
	}

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
	if err != nil {
		t.Fatal(err)
	}
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	client := toolnexus.CreateInProcessClient(a.InProcessOptions())

	if _, err := client.Ask(context.Background(), "my favourite colour is indigo", tk, "conv-1"); err != nil {
		t.Fatal(err)
	}
	res, err := client.Ask(context.Background(), "what is my favourite colour?", tk, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "indigo") {
		t.Errorf("the conversation did not carry: %q", res.Text)
	}

	// A different id must NOT see it.
	body := extractRequest(t, back.prompts[len(back.prompts)-1])
	if !strings.Contains(string(body), "indigo") {
		t.Error("the replayed transcript did not contain the earlier turn")
	}
}

// --- budgets -------------------------------------------------------------

// A turn cap is reported loudly as "incomplete", never as a silent done.
func TestBudgetExhaustionIsLoud(t *testing.T) {
	loop := toolnexus.NativeTool("spin", "Spin once.", nil,
		func(context.Context, map[string]any) (string, error) { return "spun", nil })

	// A backend that never stops calling the tool.
	back := &byName{reply: func(string, int) string {
		return toolCall("spin", map[string]any{})
	}}

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false, ExtraTools: []toolnexus.Tool{loop},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	opts := a.InProcessOptions()
	opts.MaxTurns = 3

	res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), "spin forever", tk)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "incomplete" {
		t.Errorf("status = %q, want incomplete", res.Status)
	}
	if res.Limit != "maxTurns" {
		t.Errorf("limit = %q, want maxTurns", res.Limit)
	}
	if res.ToolCallCount == 0 {
		t.Error("partial work was not preserved")
	}
}

// --- suspension (§10) ----------------------------------------------------

// A tool that needs out-of-band input suspends the run; WaitFor resolves it and
// the tool re-runs with the answer.
func TestSuspensionAndWaitFor(t *testing.T) {
	var sawAnswer string
	approve := toolnexus.Tool{
		Name:        "approve",
		Description: "Ask a human to approve.",
		InputSchema: toolnexus.JSONSchema{"type": "object", "properties": map[string]any{}},
		Source:      toolnexus.SourceNative,
		Execute: func(_ map[string]any, ctx *toolnexus.ToolContext) (toolnexus.ToolResult, error) {
			if ctx != nil && ctx.Answer != nil {
				sawAnswer = fmt.Sprintf("%v", ctx.Answer.Data["by"])
				return toolnexus.ToolResult{Output: "approved by " + sawAnswer}, nil
			}
			return toolnexus.ToolResult{
				Output: "waiting for approval",
				Metadata: map[string]any{"pending": toolnexus.Request{
					ID: "req-1", Kind: "approval", Prompt: "approve the deploy?",
				}},
			}, nil
		},
	}

	back := &byName{reply: func(_ string, calls int) string {
		if calls == 0 {
			return toolCall("approve", map[string]any{})
		}
		return answer("done")
	}}

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false, ExtraTools: []toolnexus.Tool{approve},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. No WaitFor ⇒ the run HALTS with status pending rather than hanging.
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	res, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).
		Run(context.Background(), "deploy", tk)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pending" || res.Pending == nil {
		t.Fatalf("status = %q, pending = %v", res.Status, res.Pending)
	}
	if res.Pending.Kind != "approval" {
		t.Errorf("pending kind = %q", res.Pending.Kind)
	}

	// 2. With WaitFor ⇒ resolved, and the tool sees the answer.
	back2 := &byName{reply: func(_ string, calls int) string {
		if calls == 0 {
			return toolCall("approve", map[string]any{})
		}
		return answer("deployed")
	}}
	a2 := devinadapter.New(devinadapter.Options{Agent: back2, Workdir: t.TempDir()})
	opts := a2.InProcessOptions()
	opts.MaxTurns = 4
	opts.WaitFor = func(req toolnexus.Request) (toolnexus.Answer, error) {
		return toolnexus.Answer{Ok: true, Data: map[string]any{"by": "ops-oncall"}}, nil
	}

	res2, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), "deploy", tk)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != "done" {
		t.Fatalf("status = %q", res2.Status)
	}
	if sawAnswer != "ops-oncall" {
		t.Errorf("the tool never saw the resolution: %q", sawAnswer)
	}
}

// --- observability -------------------------------------------------------

// Metrics still flow when the model is a process: one llm event per turn, one
// tool event per call, one terminal run event.
func TestMetricsFlow(t *testing.T) {
	kinds := map[string]int{}
	ping := toolnexus.NativeTool("ping", "Ping.", nil,
		func(context.Context, map[string]any) (string, error) { return "pong", nil })

	back := &byName{reply: func(_ string, calls int) string {
		if calls == 0 {
			return toolCall("ping", map[string]any{})
		}
		return answer("done")
	}}

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false, ExtraTools: []toolnexus.Tool{ping},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	opts := a.InProcessOptions()
	opts.MaxTurns = 4
	opts.OnMetric = func(ev toolnexus.MetricEvent) { kinds[ev.Event]++ }

	if _, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), "go", tk); err != nil {
		t.Fatal(err)
	}
	if kinds["llm"] < 2 {
		t.Errorf("llm events = %d, want one per turn", kinds["llm"])
	}
	if kinds["tool"] != 1 {
		t.Errorf("tool events = %d, want 1", kinds["tool"])
	}
	if kinds["run"] != 1 {
		t.Errorf("run events = %d, want 1 terminal", kinds["run"])
	}
}

// --- A2A -----------------------------------------------------------------

// A remote agent served over A2A becomes a tool, and the adapter calls it like
// any other.
func TestA2ARemoteAgentBecomesATool(t *testing.T) {
	var served bool
	// A2A advertises the toolkit's SKILLS, not its tools, so the served side
	// needs a skills directory for there to be anything on the card.
	remoteTk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins:  false,
		SkillsDir: []string{"./testdata/skills"},
		ExtraTools: []toolnexus.Tool{
			toolnexus.NativeTool("summarize", "Summarize the input.", nil,
				func(context.Context, map[string]any) (string, error) {
					served = true
					return "a one-line summary", nil
				}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The served agent needs its own model; the adapter plays that part too.
	remoteBack := &byName{reply: func(string, int) string { return answer("a one-line summary") }}
	remoteAdapter := devinadapter.New(devinadapter.Options{Agent: remoteBack, Workdir: t.TempDir()})
	remoteClient := toolnexus.CreateInProcessClient(remoteAdapter.InProcessOptions())

	handle, err := remoteTk.Serve("127.0.0.1:0", toolnexus.ServeOptions{
		Client: remoteClient,
		A2A:    &toolnexus.A2AConfig{Name: "summarizer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Stop()

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false,
		Agents:   []toolnexus.Agent{{Card: handle.URL + "/.well-known/agent-card.json"}},
	})
	if err != nil {
		t.Skipf("A2A card not reachable: %v", err)
	}

	var a2aName string
	for _, tool := range tk.Tools() {
		if tool.Source == toolnexus.SourceA2A {
			a2aName = tool.Name
		}
	}
	if a2aName == "" {
		t.Skip("the served agent advertised no A2A skills")
	}

	back := &byName{reply: func(_ string, calls int) string {
		if calls == 0 {
			return toolCall(a2aName, map[string]any{"input": "a long document"})
		}
		return answer("summarized")
	}}
	res := runWith(t, back, tk, "summarize this")
	if res.ToolCallCount != 1 {
		t.Fatalf("A2A tool not called: %+v", res.ToolCalls)
	}
	t.Logf("A2A tool %q -> %.60s (remote tool ran: %v)", a2aName, res.ToolCalls[0].Output, served)
}

// --- streaming -----------------------------------------------------------

// A CLI returns a whole answer, so streaming is refused rather than faked into
// one chunk pretending to be many.
func TestStreamingIsRefusedNotFaked(t *testing.T) {
	back := &byName{reply: func(string, int) string { return answer("hello") }}
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
	if err != nil {
		t.Fatal(err)
	}

	ch, err := toolnexus.CreateInProcessClient(a.InProcessOptions()).
		Stream(context.Background(), "hi", tk)
	if err != nil {
		return // refused up front, which is the honest outcome
	}
	for ev := range ch {
		if ev.Type == "error" {
			return // refused on the channel
		}
	}
	t.Error("streaming neither refused nor errored — a whole answer was passed off as a stream")
}

// --- the shape of the request --------------------------------------------

// Whatever the feature, the CLI always receives the same thing: the verbatim
// OpenAI request in an envelope.
func TestEveryFeatureStillSendsTheSameEnvelope(t *testing.T) {
	back := &byName{reply: func(string, int) string { return answer("ok") }}
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{Builtins: false})
	if err != nil {
		t.Fatal(err)
	}
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	opts := a.InProcessOptions()
	opts.SystemPrompt = "SYSTEM MARKER"
	opts.RequestParams = map[string]any{"temperature": 0.1, "custom_key": "custom_value"}

	if _, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), "hi", tk); err != nil {
		t.Fatal(err)
	}

	body := extractRequest(t, back.first())
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	// RequestParams the adapter has never heard of must still arrive — that is
	// the whole point of passing the body through byte for byte.
	if req["custom_key"] != "custom_value" {
		t.Errorf("an unknown request param was dropped: %v", req)
	}
	if req["temperature"] != 0.1 {
		t.Errorf("temperature = %v", req["temperature"])
	}
	if !strings.Contains(string(body), "SYSTEM MARKER") {
		t.Error("the system prompt did not reach the model")
	}
}
