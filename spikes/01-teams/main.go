// Spike 01 — sub-agent teams: does delegation via the built-in `task` tool
// actually work on a live model, and does the child's usage roll up?
//
// Proves or disproves, for the platform's per-step team support:
//   - a parent with Team gets a `task` tool; without Team it does not
//   - the child runs on its own transcript with ONLY its scoped tools
//   - where the WHOLE-TREE token total actually lives
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"
)

func main() {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Fatal("OPENROUTER_API_KEY unset")
	}
	model := envOr("SPIKE_MODEL", "anthropic/claude-haiku-4.5")

	// the child's only tool — if the child uses it, scoping works
	var lookupCalls int
	lookup := tn.NativeTool("lookup_code", "Return the source of the named function.",
		tn.JSONSchema{"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string"},
		}, "required": []string{"name"}, "additionalProperties": false},
		func(_ context.Context, args map[string]any) (string, error) {
			lookupCalls++
			return "func apply_discount(subtotal, percent):\n    return subtotal - (subtotal * percent)", nil
		})

	explore := agents.New("explore", agents.Spec{
		Does:  "read-only research: looks up source code by symbol name",
		Soul:  "You look up code and report exactly what you found. No opinions.",
		Tools: []tn.Tool{lookup},
	})

	// the parent has NO tools of its own — anything it learns must come via task
	lead := agents.New("lead", agents.Spec{
		Does:   "diagnoses bugs by delegating research",
		Soul:   "You diagnose bugs. You cannot read code yourself — delegate to your team, then answer.",
		Team:   []*agents.Agent{explore},
		Budget: &agents.Budget{MaxTokens: 40000, MaxTurns: 8},
	})

	// observability: every tool call the WHOLE tree makes
	var mu sync.Mutex
	var toolCalls []string
	onMetric := func(ev tn.MetricEvent) {
		if ev.Event == "tool" {
			mu.Lock()
			toolCalls = append(toolCalls, ev.Tool)
			mu.Unlock()
		}
	}

	llm := &agents.LLMOptions{BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, APIKey: key, Model: model}

	res, rt := lead.Run(agents.Options{LLM: llm, OnMetric: onMetric}, "What is wrong with apply_discount? Delegate the lookup, then answer in one sentence.")

	fmt.Println("== spike 01: teams ==")
	fmt.Printf("status               : %s\n", res.Status)
	fmt.Printf("turns (parent)       : %d\n", res.Turns)
	fmt.Printf("TaskResult.TotalTokens: %d   <- PARENT LLM USAGE ONLY (r.Usage.TotalTokens)\n", res.TotalTokens)
	fmt.Printf("rt.UsageTokens(Root) : %d   <- WHOLE-TREE rollup (parent + every child)\n", rt.UsageTokens(rt.Root))
	fmt.Printf("tool calls (tree)    : %v\n", toolCalls)
	fmt.Printf("child tool called    : %d time(s)  <- proves the child ran with its scoped tool\n", lookupCalls)
	fmt.Printf("text                 : %s\n", trunc(res.Text, 300))

	fmt.Println("\n-- runtime trace --")
	for _, l := range rt.Trace() {
		fmt.Println("  " + l)
	}

	// control: the same agent with no team must have no task tool
	solo := agents.New("solo", agents.Spec{Does: "x", Soul: "Answer in one word."})
	sreg := solo.Registry()
	lreg := lead.Registry()
	fmt.Printf("\ncontrol: solo registry=%v team=%v (empty team => no task tool)\n", keys(sreg), sreg["solo"].Team)
	fmt.Printf("control: lead registry=%v team=%v (transitive closure incl. child)\n", keys(lreg), lreg["lead"].Team)
}

func keys(m map[string]agents.Def) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
