// Spike 06 — agents.Completion{Verify, MaxAttempts}: the gate behind submit_output.
//
//	(1) Verify fails the FIRST time -> the loop re-runs with the REASON fed back,
//	    and the second attempt satisfies it.
//	(2) Verify never passes -> status "incomplete", Result.Limit == "completion",
//	    and a human-readable StoppedBy.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"
)

var ctx = context.Background()

type submission struct {
	Summary  string `json:"summary"`
	Severity string `json:"severity,omitempty"`
}

func main() {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Fatal("OPENROUTER_API_KEY unset")
	}
	model := envOr("SPIKE_MODEL", "anthropic/claude-haiku-4.5")

	run := func(label string, alwaysFail bool, maxAttempts int) {
		var mu sync.Mutex
		var subs []submission
		var prompts []string

		submit := tn.NativeTool("submit_output", "Submit your final structured output. This is the ONLY way to finish.",
			tn.JSONSchema{"type": "object", "properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"severity": map[string]any{"type": "string", "enum": []any{"low", "medium", "high"}},
			}, "required": []string{"summary"}, "additionalProperties": false},
			func(_ context.Context, args map[string]any) (string, error) {
				b, _ := json.Marshal(args)
				var s submission
				_ = json.Unmarshal(b, &s)
				mu.Lock()
				subs = append(subs, s)
				mu.Unlock()
				return "accepted", nil
			})

		verify := func(r tn.RunResult) (bool, string) {
			mu.Lock()
			defer mu.Unlock()
			if alwaysFail {
				return false, "the output must also carry a `confidence_interval`, which the schema does not allow"
			}
			if len(subs) == 0 {
				return false, "you never called submit_output"
			}
			last := subs[len(subs)-1]
			if last.Severity == "" {
				return false, "the submission is missing the required `severity` field (low|medium|high)"
			}
			return true, ""
		}

		agent := agents.New("reporter", agents.Spec{
			Does:       "writes a structured bug summary",
			Soul:       "You summarise bugs. Finish ONLY by calling submit_output.",
			Tools:      []tn.Tool{submit},
			Completion: &agents.Completion{Verify: verify, MaxAttempts: maxAttempts},
		})

		tk, err := tn.CreateToolkit(ctx, tn.Options{Builtins: false, ExtraTools: []tn.Tool{submit}})
		if err != nil {
			log.Fatal(err)
		}
		opts := tn.ClientOptions{
			BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, Model: model, APIKey: key,
			SystemPrompt: agent.Spec.Soul, MaxTurns: 6,
			Hooks: &tn.Hooks{BeforeLLM: func(_ context.Context, ev tn.BeforeLLMEvent) (*tn.LLMOverride, error) {
				// record every USER turn the loop sends — the re-prompt is what we want to see
				for _, m := range ev.Messages {
					b, _ := json.Marshal(m)
					var mm map[string]any
					_ = json.Unmarshal(b, &mm)
					if mm["role"] == "user" {
						s, _ := mm["content"].(string)
						mu.Lock()
						if len(prompts) == 0 || prompts[len(prompts)-1] != s {
							prompts = append(prompts, s)
						}
						mu.Unlock()
					}
				}
				return nil, nil
			}},
		}

		loop := agent.Loop(opts, tk)
		out, err := loop.Run(ctx, "Summarise: checkout 500s on coupon codes with a trailing space. Call submit_output with just a summary.", agents.RunOpts{})

		fmt.Printf("\n== %s ==\n", label)
		fmt.Printf("err            : %v\n", err)
		fmt.Printf("Outcome.Status : %q\n", out.Status)
		fmt.Printf("Outcome.Attempts: %d\n", out.Attempts)
		fmt.Printf("Outcome.Turns  : %d\n", out.Turns)
		fmt.Printf("Outcome.StoppedBy: %q\n", out.StoppedBy)
		fmt.Printf("Result.Limit   : %q\n", out.Result.Limit)
		fmt.Printf("Result.Status  : %q\n", out.Result.Status)
		fmt.Printf("Outcome.Text   : %s\n", oneline(out.Text))
		mu.Lock()
		fmt.Printf("submissions    : %s\n", mustJSON(subs))
		fmt.Println("user turns the model saw (the re-prompt IS the gate's reason):")
		for i, p := range prompts {
			fmt.Printf("   [%d] %s\n", i, oneline(p))
		}
		mu.Unlock()
	}

	run("(1) Verify fails once, then passes (MaxAttempts=3)", false, 3)
	run("(2) Verify never passes (MaxAttempts=2) -> incomplete", true, 2)
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func oneline(s string) string { return strings.Join(strings.Fields(s), " ") }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
