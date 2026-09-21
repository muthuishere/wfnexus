package devinadapter

// Translation between what toolnexus sends (an OpenAI chat request) and what a
// one-shot CLI understands (one block of text in, prose out).
//
// The CLI is asked for an OpenAI assistant message, which the adapter then
// VALIDATES. An unparseable reply is not massaged into something plausible —
// it is handed back to the CLI with the parse error so it can correct itself,
// and once the repair budget runs out it becomes an error. Guessing at a
// malformed reply is how a dropped tool call turns into a confidently wrong
// answer.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrUnparseable is returned by ParseReply when the CLI's output is not a
// usable OpenAI assistant message. Adapter turns it into a repair attempt,
// and ultimately into a failed run.
var ErrUnparseable = errors.New("devinadapter: reply is not an OpenAI assistant message")

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

// BuildPrompt puts the response contract above the transcript. tools is the
// OpenAI tools array exactly as toolnexus serialized it — including the
// `skill` tool, which is how skills get invoked. Pass nil when the run has no
// tools; the contract then asks for a content-only message.
func BuildPrompt(base string, tools json.RawMessage) string {
	var b strings.Builder

	if len(tools) > 0 {
		b.WriteString("You are the MODEL behind an OpenAI-compatible endpoint. A host loop relays your reply. The CALLER executes these functions for you — you cannot run them yourself, and you must NOT use any tools of your own to do the task:\n\n")
		b.Write(tools)
		b.WriteString("\n\n")
	} else {
		b.WriteString("You are the MODEL behind an OpenAI-compatible endpoint. A host loop relays your reply.\n\n")
	}

	b.WriteString("Reply with ONLY one fenced json block — an OpenAI assistant message — and no prose outside the fence:\n\n" +
		"```json\n" +
		`{"role":"assistant","content":"your answer here","tool_calls":[]}` + "\n" +
		"```\n\n" +
		"Rules:\n" +
		"- To call functions, put them in tool_calls and set content to null:\n" +
		"  " + `{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"NAME","arguments":{...}}}]}` + "\n" +
		"- `content` is plain prose or null — NEVER structured data. Data a function takes goes in that call's `arguments`. Describing a call in `content` does nothing; it is not a call.\n" +
		"- Put ALL independently runnable calls in one array. Never guess a value that must come from another call's result — make the prerequisite call first and wait for it.\n" +
		"- Every call you already made appears above with its TOOL RESULT. Never repeat a call whose result is already there; use the result.\n" +
		"- Finish with a content-only message once the work is done.\n\n")

	b.WriteString(base)
	return b.String()
}

// BuildRepairPrompt asks the CLI to fix a reply that failed validation. It
// restates the offending output and the exact complaint rather than the whole
// task, so a repair turn stays cheap.
func BuildRepairPrompt(bad string, cause error) string {
	return "Your previous reply could not be parsed: " + cause.Error() + "\n\n" +
		"It was:\n---\n" + strings.TrimSpace(bad) + "\n---\n\n" +
		"Reply again with ONLY one fenced json block holding an OpenAI assistant message: " +
		`{"role":"assistant","content":string|null,"tool_calls":[{"type":"function","function":{"name":string,"arguments":object}}]}` + "\n" +
		"No prose outside the fence. Do not explain the correction — just send the corrected json."
}

// fencedJSON is non-greedy on the fence but greedy on the object, so a reply
// whose JSON contains nested braces survives.
var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")

// ParseReply validates a CLI reply and converts it into an OpenAI assistant
// message plus a finish_reason. It accepts a bare assistant message, a whole
// chat.completion object (a model that produced the full response shape), and
// the legacy routsi kind/answer envelope. Anything else is ErrUnparseable —
// the caller retries or fails, and never invents an answer.
func ParseReply(text string) (message map[string]any, finishReason string, err error) {
	candidate := ""
	if m := fencedJSON.FindStringSubmatch(text); m != nil {
		candidate = m[1]
	} else if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		candidate = t
	}
	if candidate == "" {
		return nil, "", fmt.Errorf("%w: no fenced json object in the reply", ErrUnparseable)
	}

	var obj map[string]any
	if e := json.Unmarshal([]byte(candidate), &obj); e != nil {
		return nil, "", fmt.Errorf("%w: invalid json (%v)", ErrUnparseable, e)
	}

	// A whole chat.completion: unwrap choices[0].message.
	if choices, ok := obj["choices"].([]any); ok && len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			if m, ok := c["message"].(map[string]any); ok {
				obj = m
			}
		}
	}

	// The legacy routsi envelope, still accepted so replies stay compatible
	// with llm-forward-proxy's emulation.
	if kind, ok := obj["kind"].(string); ok && obj["role"] == nil {
		return fromLegacyEnvelope(obj, kind)
	}

	calls, e := normalizeToolCalls(obj["tool_calls"])
	if e != nil {
		return nil, "", e
	}
	if len(calls) > 0 {
		return map[string]any{"role": "assistant", "content": nil, "tool_calls": calls}, "tool_calls", nil
	}

	content, ok := obj["content"]
	if !ok {
		return nil, "", fmt.Errorf("%w: object has neither content nor tool_calls", ErrUnparseable)
	}
	s, ok := content.(string)
	if !ok {
		if content == nil {
			return nil, "", fmt.Errorf("%w: content is null and tool_calls is empty — say something or call something", ErrUnparseable)
		}
		// Structured data in content is the exact mistake the contract warns
		// about, so the complaint names it precisely and the model gets a
		// chance to move it into arguments.
		return nil, "", fmt.Errorf("%w: content must be a plain string, got %T — structured data belongs in a tool call's arguments", ErrUnparseable, content)
	}
	if strings.TrimSpace(s) == "" {
		return nil, "", fmt.Errorf("%w: content is empty and tool_calls is empty", ErrUnparseable)
	}
	return map[string]any{"role": "assistant", "content": s}, "stop", nil
}

// fromLegacyEnvelope handles routsi's {"kind":…,"answer":…} shape.
func fromLegacyEnvelope(obj map[string]any, kind string) (map[string]any, string, error) {
	// tool_calls win over kind: models emit kind:"answer" alongside a populated
	// tool_calls array, and honouring kind there drops the call.
	calls, err := normalizeToolCalls(obj["tool_calls"])
	if err != nil {
		return nil, "", err
	}
	if len(calls) > 0 {
		return map[string]any{"role": "assistant", "content": nil, "tool_calls": calls}, "tool_calls", nil
	}
	if kind != "answer" {
		return nil, "", fmt.Errorf("%w: kind=%q with no tool_calls", ErrUnparseable, kind)
	}
	s, ok := obj["answer"].(string)
	if !ok {
		return nil, "", fmt.Errorf("%w: answer must be a plain string, got %T", ErrUnparseable, obj["answer"])
	}
	return map[string]any{"role": "assistant", "content": s}, "stop", nil
}

// normalizeToolCalls accepts the shapes models actually produce: the nested
// OpenAI {"function":{"name","arguments"}} form and the flat {"name","args"}
// form, with arguments as an object or as a JSON-encoded string.
func normalizeToolCalls(v any) ([]any, error) {
	if v == nil {
		return nil, nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: tool_calls must be an array, got %T", ErrUnparseable, v)
	}

	var out []any
	for i, item := range raw {
		c, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: tool_calls[%d] is not an object", ErrUnparseable, i)
		}
		fn, _ := c["function"].(map[string]any)
		if fn == nil {
			fn = c // flat form
		}
		name, _ := fn["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("%w: tool_calls[%d] has no function name", ErrUnparseable, i)
		}

		args := fn["arguments"]
		if args == nil {
			args = fn["args"]
		}
		encoded := "{}"
		switch a := args.(type) {
		case nil:
		case string:
			if strings.TrimSpace(a) != "" {
				encoded = a // already JSON-encoded
			}
		default:
			b, err := json.Marshal(a)
			if err != nil {
				return nil, fmt.Errorf("%w: tool_calls[%d] arguments not encodable: %v", ErrUnparseable, i, err)
			}
			encoded = string(b)
		}

		id, _ := c["id"].(string)
		if id == "" {
			id = fmt.Sprintf("call_%s_%d", randHex(), i)
		}
		out = append(out, map[string]any{
			"id":       id,
			"type":     "function",
			"function": map[string]any{"name": name, "arguments": encoded},
		})
	}
	return out, nil
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
