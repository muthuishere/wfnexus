// Spike 03 — suspension & durable resume. THE spike our `needs_input` depends on.
//
// Four parts, all against the live wire:
//   A  tn.Client path, native tool returns tn.Pending, NO WaitFor -> RunResult{Status,Pending}
//   B  tn.Client resume: Client.RunWithAnswer(ctx, tk, history, pending, answer)
//   C  agents runtime path, NO WaitFor -> TaskResult{Status,Pending} (+ data.path)
//   D  agents runtime resume: Runtime.Resume(tn.Answer{...})
// plus E, the built-in `question` tool on the client path.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"
)

var ctx = context.Background()

// askHuman suspends the first time; on the post-Answer retry it reads
// ToolContext.Answer and returns the human's value. Raw tn.Tool, NOT
// tn.NativeTool — NativeTool's fn signature has no access to ToolContext.Answer.
func askHuman(execs *int32) tn.Tool {
	return tn.Tool{
		Name:        "ask_human",
		Description: "Ask the human operator for a value you cannot determine yourself.",
		InputSchema: tn.JSONSchema{"type": "object", "properties": map[string]any{
			"question": map[string]any{"type": "string"},
		}, "required": []any{"question"}},
		Source: tn.SourceCustom,
		Execute: func(args map[string]any, tc *tn.ToolContext) (tn.ToolResult, error) {
			atomic.AddInt32(execs, 1)
			q, _ := args["question"].(string)
			if tc != nil && tc.Answer != nil {
				v, _ := tc.Answer.Data["value"].(string)
				return tn.ToolResult{Output: "human answered: " + v}, nil
			}
			return tn.Pending(tn.Request{
				Kind:   "input",
				Prompt: q,
				Data:   map[string]any{"field": "db_password", "format": "string"},
			}), nil
		},
	}
}

func main() {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Fatal("OPENROUTER_API_KEY unset")
	}
	model := envOr("SPIKE_MODEL", "anthropic/claude-haiku-4.5")
	prompt := "Use ask_human to ask which environment to deploy to, then state the answer in one short line."

	// ---------------------------------------------------------------- A + B
	var execsA int32
	tk, err := tn.CreateToolkit(ctx, tn.Options{Builtins: false, ExtraTools: []tn.Tool{askHuman(&execsA)}})
	must(err)
	client := tn.CreateClient(tn.ClientOptions{
		BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, Model: model, APIKey: key,
		SystemPrompt: "You always use ask_human before deciding anything about environments.",
		MaxTurns:     6,
		// WaitFor deliberately NOT set -> durable halt
	})
	resA, err := client.Run(ctx, prompt, tk)
	must(err)
	fmt.Println("== A: tn.Client, no WaitFor ==")
	fmt.Printf("RunResult.Status   : %q\n", resA.Status)
	fmt.Printf("RunResult.Text     : %q  <- pendingRun sets Text = Request.Prompt\n", resA.Text)
	fmt.Printf("RunResult.Pending  : %s\n", jsn(resA.Pending))
	fmt.Printf("messages persisted : %d (this []any IS the durable transcript)\n", len(resA.Messages))
	fmt.Printf("tool body executed : %d time(s)\n", execsA)

	fmt.Println("\n== B: tn.Client resume via Client.RunWithAnswer ==")
	ansB := tn.Answer{ID: resA.Pending.ID, Ok: true, Data: map[string]any{"value": "staging"}}
	resB, err := client.RunWithAnswer(ctx, tk, resA.Messages, *resA.Pending, ansB)
	if err != nil {
		fmt.Printf("RunWithAnswer ERROR: %v\n", err)
	} else {
		fmt.Printf("Status   : %q\n", resB.Status)
		fmt.Printf("Text     : %s\n", oneline(resB.Text))
		fmt.Printf("tool body executed total: %d\n", execsA)
		fmt.Println("NOTE: RunWithAnswer does NOT re-execute the tool — it SPLICES")
		fmt.Println("      Answer.Data[\"output\"] in as the tool_result and continues.")
	}

	// B2: same resume, but supplying the tool output the relay way.
	var execsB2 int32
	tk2, err := tn.CreateToolkit(ctx, tn.Options{Builtins: false, ExtraTools: []tn.Tool{askHuman(&execsB2)}})
	must(err)
	resA2, err := client.Run(ctx, prompt, tk2)
	must(err)
	ansB2 := tn.Answer{ID: resA2.Pending.ID, Ok: true,
		Data: map[string]any{tn.RelayOutputKey: "human answered: staging", tn.RelayIsErrorKey: false}}
	resB2, err := client.RunWithAnswer(ctx, tk2, resA2.Messages, *resA2.Pending, ansB2)
	fmt.Println("\n== B2: resume supplying Answer.Data[\"output\"] (the shape that WORKS) ==")
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
	} else {
		fmt.Printf("Status: %q  text: %s\n", resB2.Status, oneline(resB2.Text))
		fmt.Printf("tool body executed: %d (0 extra => spliced, not re-run)\n", execsB2)
	}

	// mismatched-id guard
	_, errMM := client.RunWithAnswer(ctx, tk2, resA2.Messages, *resA2.Pending, tn.Answer{ID: "bogus", Ok: true})
	fmt.Printf("stale-answer guard : %v\n", errMM)

	// ---------------------------------------------------------------- C + D
	var execsC int32
	worker := agents.New("worker", agents.Spec{
		Does:  "deploys things after asking the operator",
		Soul:  "You always use ask_human before deciding anything about environments.",
		Tools: []tn.Tool{askHuman(&execsC)},
		// WaitFor deliberately NOT set anywhere in the tree -> durable park
		Budget: &agents.Budget{MaxTurns: 6, MaxTokens: 40000},
	})
	llm := &agents.LLMOptions{BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, APIKey: key, Model: model}
	resC, rt := worker.Run(agents.Options{LLM: llm}, prompt)

	fmt.Println("\n== C: agents runtime, no WaitFor ==")
	fmt.Printf("TaskResult.Status  : %q\n", resC.Status)
	fmt.Printf("TaskResult.Pending : %s\n", jsn(resC.Pending))
	fmt.Printf("handles            : %s\n", jsn(rt.List()))
	fmt.Printf("tool body executed : %d\n", execsC)

	fmt.Println("\n== D: agents runtime resume via Runtime.Resume(tn.Answer) ==")
	errD := rt.Resume(tn.Answer{ID: resC.Pending.ID, Ok: true, Data: map[string]any{"value": "staging"}})
	fmt.Printf("Resume err   : %v\n", errD)
	after := rt.List()
	fmt.Printf("handles after: %s\n", jsn(after))
	fmt.Printf("tool body executed total: %d  <- >1 means the TURN REPLAYS and the tool RE-RUNS\n", execsC)
	for _, l := range rt.Trace() {
		fmt.Println("  trace: " + l)
	}
	// Resume returns only an error; the resumed result is read off the runtime.
	root := rt.Root
	_ = root

	// ---------------------------------------------------------------- E
	fmt.Println("\n== E: the BUILT-IN `question` tool, client path, no WaitFor ==")
	tkQ, err := tn.CreateToolkit(ctx, tn.Options{Builtins: map[string]any{"tools": map[string]any{
		"bash": false, "read": false, "write": false, "edit": false, "glob": false,
		"grep": false, "webfetch": false, "apply_patch": false, "todowrite": false,
	}}})
	must(err)
	defer tkQ.Close()
	fmt.Printf("toolkit tools: %v\n", toolNames(tkQ))
	cq := tn.CreateClient(tn.ClientOptions{
		BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, Model: model, APIKey: key,
		SystemPrompt: "You must call the `question` tool before answering.", MaxTurns: 4,
	})
	resE, err := cq.Run(ctx, "Which environment should I deploy to? Ask me with the question tool first.", tkQ)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
	} else {
		fmt.Printf("Status : %q\n", resE.Status)
		fmt.Printf("Pending: %s\n", jsn(resE.Pending))
	}
}

func toolNames(tk *tn.Toolkit) []string {
	var out []string
	for _, t := range tk.Tools() {
		out = append(out, t.Name)
	}
	return out
}

func jsn(v any) string { b, _ := json.MarshalIndent(v, "  ", "  "); return string(b) }

func oneline(s string) string { return strings.Join(strings.Fields(s), " ") }

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
