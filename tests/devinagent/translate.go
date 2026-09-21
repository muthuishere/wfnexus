package devinadapter

// Translation between what toolnexus sends (an OpenAI chat request) and what a
// one-shot CLI understands (one block of text in, prose out).
//
// The fenced-JSON tool protocol is routsi's, verbatim
// (llm-forward-proxy/internal/backend/toolemu.go), so replies stay compatible
// between the two.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// RenderTranscript flattens the OpenAI message array into text a one-shot CLI
// can read. Tool results get their own labelled block, so turn 2 of the loop
// still sees what turn 1 produced — that is what makes multi-turn tool use
// work against a CLI with no conversation state.
func RenderTranscript(messages []json.RawMessage) string {
	var b strings.Builder
	// A tool result identifies itself by tool_call_id, not by name, so the
	// names are learned from the assistant turn that made the calls. Without
	// this the CLI is handed an unlabelled result and has to guess which of
	// several parallel calls it answers.
	names := map[string]string{}
	for _, raw := range messages {
		var m struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			Name       string          `json:"name"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		text := contentText(m.Content)
		switch m.Role {
		case "system":
			// The system message carries the skills catalog toolnexus injected;
			// it leads, unlabelled, exactly as a system prompt would.
			b.WriteString(text + "\n\n")
		case "tool":
			name := m.Name
			if name == "" {
				name = names[m.ToolCallID]
			}
			if name == "" {
				name = "tool"
			}
			fmt.Fprintf(&b, "TOOL RESULT (%s):\n%s\n\n", name, text)
		case "assistant":
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					names[tc.ID] = tc.Function.Name
				}
				fmt.Fprintf(&b, "ASSISTANT CALLED: %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
			}
			if text != "" {
				fmt.Fprintf(&b, "ASSISTANT: %s\n", text)
			}
			b.WriteString("\n")
		default:
			fmt.Fprintf(&b, "USER:\n%s\n\n", text)
		}
	}
	return strings.TrimSpace(b.String())
}

// contentText accepts both content shapes: a plain string and the parts array.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

// BuildToolPrompt states the fenced-JSON contract above the transcript. tools
// is the OpenAI tools array as toolnexus serialized it — including the `skill`
// tool, which is how skills get invoked.
func BuildToolPrompt(base string, tools json.RawMessage) string {
	var b strings.Builder
	b.WriteString("You are answering an API request relayed by a host loop. The CALLER executes these functions for you — you cannot run them yourself, and you must NOT use any tools of your own to do the task:\n\n")
	b.Write(tools)
	b.WriteString("\n\nReply with ONLY a fenced json block matching " +
		`{"kind":"answer"|"tool_calls","answer":string,"tool_calls":[{"name":string,"arguments":object}]}` +
		" — no prose outside the fence. Emit kind=tool_calls with ALL independently runnable calls in one array when the request needs actions; NEVER guess values that must come from another call's result (make the prerequisite call first). Otherwise kind=answer.\n" +
		// Both drifts below were observed from a live devin run: the payload
		// placed in `answer` as an object, and a function called by writing its
		// arguments into `answer` instead of into tool_calls.
		"`answer` MUST be a plain string of prose. Structured data NEVER goes in `answer` — if a function takes it, that is a tool_calls entry with the data in `arguments`. Calling a function by describing it in `answer` does nothing.\n\n")
	b.WriteString(base)
	return b.String()
}

// fencedJSON is non-greedy on the fence but greedy on the object, so a reply
// whose JSON contains nested braces survives.
var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")

// ParseToolReply maps a CLI's prose reply onto an OpenAI assistant message and
// its finish_reason. A non-empty tool_calls array wins over kind; anything
// unparseable degrades to plain content rather than failing the run — a model
// that ignored the contract still said something worth passing on.
func ParseToolReply(text string) (message map[string]any, finishReason string) {
	plain := func() (map[string]any, string) {
		return map[string]any{"role": "assistant", "content": strings.TrimSpace(text)}, "stop"
	}

	candidate := ""
	if m := fencedJSON.FindStringSubmatch(text); m != nil {
		candidate = m[1]
	} else if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		candidate = t
	}
	if candidate == "" {
		return plain()
	}

	var reply struct {
		Kind string `json:"kind"`
		// Raw, not string: a model that puts an object in `answer` must not
		// fail the whole decode and lose the tool_calls sitting beside it.
		Answer    json.RawMessage `json:"answer"`
		ToolCalls []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal([]byte(candidate), &reply) != nil {
		return plain()
	}

	// Tool calls win over kind. Models routinely emit kind:"answer" WITH a
	// populated tool_calls array (a live devin run did exactly that: it wrote
	// the answer as prose and called submit_answer in the same object).
	// Honouring kind strictly there drops the call and ends the run early, so
	// a non-empty tool_calls is taken as the intent whatever kind says. This
	// is a superset of routsi's parser — a routsi-shaped reply is unaffected.
	if len(reply.ToolCalls) > 0 {
		var calls []any
		for i, c := range reply.ToolCalls {
			if c.Name == "" {
				continue
			}
			args := string(c.Arguments)
			var asStr string
			if json.Unmarshal(c.Arguments, &asStr) == nil {
				args = asStr // the model already sent a JSON-encoded string
			}
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			calls = append(calls, map[string]any{
				"id":       fmt.Sprintf("call_%s_%d", randHex(), i),
				"type":     "function",
				"function": map[string]any{"name": c.Name, "arguments": args},
			})
		}
		if len(calls) > 0 {
			return map[string]any{"role": "assistant", "content": nil, "tool_calls": calls}, "tool_calls"
		}
	}
	if reply.Kind == "answer" {
		return map[string]any{"role": "assistant", "content": answerText(reply.Answer)}, "stop"
	}
	return plain()
}

// answerText unwraps the `answer` field: a JSON string becomes its value,
// anything else (an object, a number) is passed through as JSON text rather
// than dropped.
func answerText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
