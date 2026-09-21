// Spike 02 — guardrails: does a Spec.Guardrail denial reach the MODEL as a tool
// result (so it reacts and keeps going), and is first-deny-wins real?
//
// Proves or disproves, for the platform's per-step policy:
//   - a denied call never reaches the tool body
//   - the model sees "denied: <reason>" as an ordinary tool message
//   - with two matching guardrails, only the FIRST reason is ever produced
//   - an allowed call still runs normally in the same transcript
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

func main() {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Fatal("OPENROUTER_API_KEY unset")
	}
	model := envOr("SPIKE_MODEL", "anthropic/claude-haiku-4.5")

	var mu sync.Mutex
	var executed []string // commands that actually reached the tool body
	bash := tn.NativeTool("bash", "Run a shell command and return its output.",
		tn.JSONSchema{"type": "object", "properties": map[string]any{
			"command": map[string]any{"type": "string"},
		}, "required": []string{"command"}, "additionalProperties": false},
		func(_ context.Context, args map[string]any) (string, error) {
			cmd, _ := args["command"].(string)
			mu.Lock()
			executed = append(executed, cmd)
			mu.Unlock()
			return "(simulated) ok: " + cmd, nil
		})

	var fired []string
	rail := func(tag, reason string) agents.Guardrail {
		return func(ev tn.BeforeToolEvent) string {
			cmd, _ := ev.Args["command"].(string)
			if ev.Name == "bash" && strings.Contains(cmd, "git push") {
				mu.Lock()
				fired = append(fired, tag)
				mu.Unlock()
				return reason
			}
			return "" // "" or "allow" ⇒ permit
		}
	}

	// capture what the model actually saw, turn by turn
	var lastMessages []any
	hooks := &tn.Hooks{
		BeforeLLM: func(_ context.Context, ev tn.BeforeLLMEvent) (*tn.LLMOverride, error) {
			mu.Lock()
			lastMessages = ev.Messages
			mu.Unlock()
			return nil, nil
		},
	}

	worker := agents.New("worker", agents.Spec{
		Does:  "runs shell commands",
		Soul:  "You run shell commands with the bash tool. Run exactly what you are asked, one command per call. Then report what happened for each.",
		Tools: []tn.Tool{bash},
		Guardrails: []agents.Guardrail{
			rail("FIRST", "outward-facing git operations are owner-gated in this workflow"),
			rail("SECOND", "SECOND-GUARDRAIL-REASON (must never be seen: first deny wins)"),
		},
		Budget: &agents.Budget{MaxTurns: 6, MaxTokens: 40000},
		Hooks:  hooks,
	})

	llm := &agents.LLMOptions{BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI, APIKey: key, Model: model}
	res, _ := worker.Run(agents.Options{LLM: llm},
		"Do these two things with bash, in order: (1) run `ls -1`; (2) run `git push origin main`. Then tell me in two short lines what happened for each.")

	fmt.Println("== spike 02: guardrails ==")
	fmt.Printf("status            : %s\n", res.Status)
	fmt.Printf("turns             : %d\n", res.Turns)
	fmt.Printf("tool body ran for : %v   <- the denied command must be ABSENT\n", executed)
	fmt.Printf("guardrails fired  : %v   <- first-deny-wins => SECOND never evaluates after FIRST denies\n", fired)
	fmt.Printf("final text        :\n%s\n", res.Text)

	fmt.Println("\n-- the exact tool messages the model saw --")
	for _, m := range lastMessages {
		b, _ := json.Marshal(m)
		var mm map[string]any
		_ = json.Unmarshal(b, &mm)
		if r, _ := mm["role"].(string); r == "tool" {
			fmt.Printf("  %s\n", string(b))
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
