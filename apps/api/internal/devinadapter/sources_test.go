//go:build toolnexus_inprocess

// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag this file breaks `go build ./...` for the whole
// module. Nothing here is deleted: build or test it with
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// and drop the tag once a toolnexus release carries the export.

package devinadapter_test

// Every toolnexus tool source, driven through the adapter: native tools,
// built-ins, skills, a real MCP server over HTTP, and sub-agents. The point is
// that none of them need anything from this package — the adapter is a model,
// and toolnexus's own aggregation is what makes them all look like tools.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	devinadapter "github.com/muthuishere/bug-fixer-platform/apps/api/internal/devinadapter"
	toolnexus "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"
)

// byName replies by looking at which tools the request offers and what has
// already been done, rather than by a fixed script — a turn-indexed script
// breaks the moment a source adds a turn.
type byName struct {
	mu      sync.Mutex
	prompts []string
	reply   func(prompt string, calls int) string
	calls   int
}

func (b *byName) Name() string { return "by-name" }

func (b *byName) Execute(_ context.Context, t devinadapter.Turn) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prompts = append(b.prompts, t.Prompt)
	out := b.reply(t.Prompt, b.calls)
	b.calls++
	return out, nil
}

func (b *byName) first() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.prompts) == 0 {
		return ""
	}
	return b.prompts[0]
}

func runWith(t *testing.T, back devinadapter.Agent, tk *toolnexus.Toolkit, prompt string) toolnexus.RunResult {
	t.Helper()
	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	opts := a.InProcessOptions()
	opts.MaxTurns = 8
	res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), prompt, tk)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// --- built-in tools ------------------------------------------------------

// The 10 built-ins are on by default. They must reach the CLI in the request
// and execute for real when called.
func TestBuiltinToolsWork(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hello.txt")

	back := &byName{reply: func(_ string, calls int) string {
		switch calls {
		case 0:
			return toolCall("write", map[string]any{"path": target, "content": "written by the builtin"})
		case 1:
			return toolCall("read", map[string]any{"path": target})
		default:
			return answer("done")
		}
	}}

	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{}) // builtins ON
	if err != nil {
		t.Fatal(err)
	}

	res := runWith(t, back, tk, "write then read the file")

	// Advertised to the model…
	first := back.first()
	for _, name := range []string{"write", "read", "bash", "grep", "glob"} {
		if !strings.Contains(first, `"name": "`+name+`"`) {
			t.Errorf("builtin %q not advertised to the CLI", name)
		}
	}
	// …and genuinely executed.
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the write builtin did not create the file: %v", err)
	}
	if string(b) != "written by the builtin" {
		t.Errorf("file holds %q", b)
	}
	if res.ToolCallCount != 2 {
		t.Errorf("tool calls = %d, want 2", res.ToolCallCount)
	}
	var sawRead bool
	for _, c := range res.ToolCalls {
		if c.Name == "read" && strings.Contains(c.Output, "written by the builtin") {
			sawRead = true
		}
	}
	if !sawRead {
		t.Errorf("the read builtin did not return the content: %+v", res.ToolCalls)
	}
}

// --- MCP -----------------------------------------------------------------

// A real MCP server over HTTP: one toolkit serves its tools at /mcp, another
// connects to it as a remote server and the model calls through.
func TestMcpServerToolsWork(t *testing.T) {
	var called int32
	var mu sync.Mutex

	upstream, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false,
		ExtraTools: []toolnexus.Tool{
			toolnexus.NativeTool("ticket_status", "Look up a ticket's status.",
				toolnexus.JSONSchema{
					"type":       "object",
					"properties": map[string]any{"id": map[string]any{"type": "string"}},
					"required":   []string{"id"},
				},
				func(_ context.Context, args map[string]any) (string, error) {
					mu.Lock()
					called++
					mu.Unlock()
					return fmt.Sprintf(`{"id":%q,"status":"open","owner":"payments"}`, args["id"]), nil
				}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	handle, err := upstream.Serve("127.0.0.1:0", toolnexus.ServeOptions{
		MCP: &toolnexus.MCPServeConfig{Name: "tickets", Version: "1.0.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Stop()

	// The consumer side: an ordinary mcp.json pointing at that server.
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false,
		McpConfig: toolnexus.McpConfig{
			"tickets": {Type: "remote", URL: handle.URL + "/mcp"},
		},
	})
	if err != nil {
		t.Fatalf("connect to the MCP server: %v", err)
	}

	// MCP tools are exposed as server_tool.
	var mcpName string
	for _, tool := range tk.Tools() {
		if tool.Source == toolnexus.SourceMCP {
			mcpName = tool.Name
		}
	}
	if mcpName == "" {
		t.Fatal("no MCP tool in the toolkit — the server did not advertise one")
	}

	back := &byName{reply: func(_ string, calls int) string {
		if calls == 0 {
			return toolCall(mcpName, map[string]any{"id": "BUG-42"})
		}
		return answer("ticket BUG-42 is open, owned by payments")
	}}

	res := runWith(t, back, tk, "what is the status of BUG-42?")

	if !strings.Contains(back.first(), mcpName) {
		t.Errorf("the MCP tool was not advertised to the CLI:\n%s", back.first())
	}
	if called != 1 {
		t.Errorf("the MCP server's tool ran %d times, want 1", called)
	}
	if res.ToolCallCount != 1 || !strings.Contains(res.ToolCalls[0].Output, `"status":"open"`) {
		t.Errorf("MCP result did not come back: %+v", res.ToolCalls)
	}
	if res.Text == "" {
		t.Error("no final answer")
	}
}

// --- sub-agents ----------------------------------------------------------

// A parent agent delegates to a teammate through the `task` tool. The sub-agent
// runtime takes a transport rather than a Generate, which is what Transport()
// is for.
func TestSubAgentsWork(t *testing.T) {
	var researcherRan bool

	lookup := toolnexus.NativeTool("lookup_owner", "Find who owns a service.", nil,
		func(context.Context, map[string]any) (string, error) {
			researcherRan = true
			return "the payments team", nil
		})

	researcher := agents.New("researcher", agents.Spec{
		Does:  "Looks up who owns a service.",
		Soul:  "You look things up and report the answer plainly.",
		Tools: []toolnexus.Tool{lookup},
	})
	lead := agents.New("lead", agents.Spec{
		Does: "Answers questions, delegating research.",
		Soul: "You delegate lookups to the researcher, then answer.",
		Team: []*agents.Agent{researcher},
	})

	// One backend serves both agents; who is asking is visible in the prompt.
	back := &byName{}
	back.reply = func(prompt string, _ int) string {
		switch {
		// The researcher's turn: it has the lookup tool.
		case strings.Contains(prompt, "lookup_owner") && !strings.Contains(prompt, "TOOL RESULT") &&
			!strings.Contains(prompt, "the payments team"):
			return toolCall("lookup_owner", map[string]any{"service": "billing"})
		case strings.Contains(prompt, "the payments team"):
			return answer("billing is owned by the payments team")
		// The lead's first turn: it has the task tool and nothing else.
		case strings.Contains(prompt, `"name": "task"`):
			return toolCall("task", map[string]any{
				"agent": "researcher", "prompt": "who owns billing?",
			})
		default:
			return answer("billing is owned by the payments team")
		}
	}

	a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
	baseURL, apiKey, model := a.AgentsLLM()

	// Runtime.Close closes a HANDLE, not the runtime, so a one-shot Run needs
	// no teardown; rt is kept only for its trace on failure.
	result, rt := lead.Run(agents.Options{
		Transport: a.Transport(),
		Registry:  lead.Registry(),
		LLM:       &agents.LLMOptions{BaseURL: baseURL, APIKey: apiKey, Style: toolnexus.StyleOpenAI, Model: model},
	}, "who owns the billing service?")

	if result.Status != "done" {
		if rt != nil {
			t.Logf("runtime trace:\n%s", strings.Join(rt.Trace(), "\n"))
		}
		t.Fatalf("status = %q, text = %q", result.Status, result.Text)
	}
	if !researcherRan {
		t.Error("the sub-agent's own tool never ran — delegation did not happen")
	}
	if !strings.Contains(strings.ToLower(result.Text), "payments") {
		t.Errorf("the lead did not report the sub-agent's finding: %q", result.Text)
	}
}

// --- everything at once --------------------------------------------------

// Native + builtin + skill + MCP in ONE toolkit, so the sources are proven to
// coexist rather than merely to work one at a time.
func TestAllSourcesInOneToolkit(t *testing.T) {
	upstream, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false,
		ExtraTools: []toolnexus.Tool{
			toolnexus.NativeTool("remote_ping", "Ping the remote service.", nil,
				func(context.Context, map[string]any) (string, error) { return "remote-pong", nil }),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := upstream.Serve("127.0.0.1:0", toolnexus.ServeOptions{
		MCP: &toolnexus.MCPServeConfig{Name: "svc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Stop()

	var got *Triage
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		SkillsDir:  []string{"./testdata/skills"},
		ExtraTools: []toolnexus.Tool{submitTool(&got)},
		McpConfig: toolnexus.McpConfig{
			"svc": {Type: "remote", URL: handle.URL + "/mcp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Every source present and distinguishable.
	bySource := map[toolnexus.ToolSource][]string{}
	for _, tool := range tk.Tools() {
		bySource[tool.Source] = append(bySource[tool.Source], tool.Name)
	}
	for _, src := range []toolnexus.ToolSource{
		toolnexus.SourceNative, toolnexus.SourceBuiltin, toolnexus.SourceSkill, toolnexus.SourceMCP,
	} {
		if len(bySource[src]) == 0 {
			t.Errorf("no tools from source %q; toolkit holds %v", src, bySource)
		}
	}

	var mcpName string
	for _, n := range bySource[toolnexus.SourceMCP] {
		mcpName = n
	}

	back := &byName{}
	back.reply = func(prompt string, calls int) string {
		switch calls {
		case 0:
			return toolCall("skill", map[string]any{"name": "bug-triage"}) // skill source
		case 1:
			return toolCall(mcpName, map[string]any{}) // mcp source
		case 2:
			return toolCall("bash", map[string]any{"command": "echo builtin-ok"}) // builtin source
		case 3:
			return toolCall("submit_answer", Triage{ // native source
				Summary: "all sources reachable", RootCause: "n/a", Severity: "low",
				Files: []string{"none"}, Fix: []string{"none"}, Confidence: 0.5,
			})
		default:
			return answer("all four sources answered")
		}
	}

	res := runWith(t, back, tk, "exercise every tool source")

	outputs := map[string]string{}
	for _, c := range res.ToolCalls {
		outputs[c.Name] = c.Output
	}
	if !strings.Contains(outputs["skill"], "Severity ladder") {
		t.Errorf("skill body not returned: %q", outputs["skill"])
	}
	if !strings.Contains(outputs[mcpName], "remote-pong") {
		t.Errorf("MCP tool did not answer: %q", outputs[mcpName])
	}
	if !strings.Contains(outputs["bash"], "builtin-ok") {
		t.Errorf("builtin did not run: %q", outputs["bash"])
	}
	if got == nil {
		t.Fatal("native structured tool never captured an answer")
	}
	if res.ToolCallCount != 4 {
		t.Errorf("tool calls = %d, want 4 (one per source)", res.ToolCallCount)
	}
}

// The request the CLI receives must carry every source's schema, since that is
// the only thing the model has to go on.
func TestEveryToolSourceReachesTheCLI(t *testing.T) {
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		SkillsDir: []string{"./testdata/skills"},
		ExtraTools: []toolnexus.Tool{
			toolnexus.NativeTool("my_native", "A native tool.", nil,
				func(context.Context, map[string]any) (string, error) { return "ok", nil }),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	back := &byName{reply: func(string, int) string { return answer("noted") }}
	runWith(t, back, tk, "hello")

	body := extractRequest(t, back.first())
	var req struct {
		Tools []struct {
			Function struct {
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	for _, tool := range req.Tools {
		names[tool.Function.Name] = true
		if tool.Function.Parameters == nil {
			t.Errorf("tool %q reached the CLI with no schema", tool.Function.Name)
		}
	}
	for _, want := range []string{"my_native", "skill", "bash", "read"} {
		if !names[want] {
			t.Errorf("%q missing from the request's tools array", want)
		}
	}
	if len(tk.Tools()) != len(req.Tools) {
		t.Errorf("toolkit has %d tools but the request carried %d", len(tk.Tools()), len(req.Tools))
	}
}
