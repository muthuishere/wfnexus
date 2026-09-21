package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// turn is one scripted model reply: either tool calls or a final message.
type turn struct {
	calls []call
	text  string
}

type call struct {
	name string
	args map[string]any
}

// fakeLLM is an OpenAI-compatible endpoint that replays a script, so a test
// exercises the REAL toolnexus agent loop (multi-turn, tool execution, result
// feedback) with a deterministic model.
type fakeLLM struct {
	*httptest.Server
	mu sync.Mutex
	// script is consumed per step: keyed by the tool the step must submit to,
	// so a retried step replays from its own queue.
	turns    []turn
	next     func() turn
	n        int
	requests []map[string]any
}

// newFakeLLMFunc replies from a function, so a test can hold a turn open.
func newFakeLLMFunc(t *testing.T, fn func() turn) *fakeLLM {
	t.Helper()
	f := &fakeLLM{next: fn}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func newFakeLLM(t *testing.T, turns ...turn) *fakeLLM {
	t.Helper()
	f := &fakeLLM{turns: turns}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeLLM) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)

	f.mu.Lock()
	f.requests = append(f.requests, req)
	if f.next != nil {
		f.n++
		f.mu.Unlock()
		tn := f.next()
		f.writeTurn(w, tn)
		return
	}
	var tn turn
	if f.n < len(f.turns) {
		tn = f.turns[f.n]
		f.n++
	} else {
		tn = turn{text: "(script exhausted)"}
	}
	f.mu.Unlock()
	f.writeTurn(w, tn)
}

func (f *fakeLLM) writeTurn(w http.ResponseWriter, tn turn) {
	msg := map[string]any{"role": "assistant"}
	if len(tn.calls) > 0 {
		var tcs []any
		for i, c := range tn.calls {
			raw, _ := json.Marshal(c.args)
			tcs = append(tcs, map[string]any{
				"id": fmt.Sprintf("call_%d_%d", f.n, i), "type": "function",
				"function": map[string]any{"name": c.name, "arguments": string(raw)},
			})
		}
		msg["content"] = nil
		msg["tool_calls"] = tcs
	} else {
		msg["content"] = tn.text
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "cmpl", "model": "fake",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
}

func (f *fakeLLM) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// toolNamesOffered returns the tool names sent to the model on request i.
func (f *fakeLLM) toolNamesOffered(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		return nil
	}
	raw, _ := f.requests[i]["tools"].([]any)
	var out []string
	for _, t := range raw {
		m, _ := t.(map[string]any)
		fn, _ := m["function"].(map[string]any)
		if n, ok := fn["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

// toolResults returns every tool-result message the model was fed, so a test
// can assert what the model actually SAW (a guardrail denial, for instance).
func (f *fakeLLM) toolResults() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, req := range f.requests {
		msgs, _ := req["messages"].([]any)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if mm["role"] != "tool" {
				continue
			}
			if c, ok := mm["content"].(string); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

// promptOf returns the concatenated user text of request i.
func (f *fakeLLM) promptOf(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		return ""
	}
	msgs, _ := f.requests[i]["messages"].([]any)
	var b strings.Builder
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] == "user" || mm["role"] == "system" {
			if c, ok := mm["content"].(string); ok {
				b.WriteString(c)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func submit(args map[string]any) turn {
	return turn{calls: []call{{name: "submit_output", args: args}}}
}

// finish is the wrap-up reply every step ends on: after submit_output the loop
// asks the model once more, and a reply with no tool calls closes the step.
func finish() turn { return turn{text: "done"} }

// bash is a scripted shell tool call.
func bashCall(cmd string) turn {
	return turn{calls: []call{{name: "bash", args: map[string]any{"command": cmd}}}}
}
