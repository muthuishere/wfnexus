// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag transport.go breaks `go build ./...` for the whole
// module, and the package is tagged as a unit so its tests keep compiling.
// Nothing is deleted:
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// Drop the tag once a toolnexus release carries the export.

package devinadapter

// Translation between what toolnexus sends (an OpenAI chat request) and what a
// one-shot CLI understands (one block of text in, prose out).
//
// EVERY request reaches the CLI the same way: the verbatim OpenAI request body
// inside an <openai_request> envelope with the response contract beside it. A
// repair is that same envelope with the rejected reply appended.
//
// The CLI is asked for an OpenAI assistant message, which the adapter then
// VALIDATES. An unparseable reply is not massaged into something plausible —
// it is handed back to the CLI with the parse error so it can correct itself,
// and once the repair budget runs out it becomes an error. Guessing at a
// malformed reply is how a dropped tool call turns into a confidently wrong
// answer.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	toolnexus "github.com/muthuishere/toolnexus/golang"
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

// BuildPrompt wraps the VERBATIM OpenAI request in an XML envelope and asks
// for the OpenAI response back.
//
// The request body is passed through byte for byte rather than re-rendered as
// prose. That keeps the exchange lossless — messages, tools, tool_choice,
// response_format, temperature and anything toolnexus starts sending later all
// reach the CLI without this package having to learn about them first. The XML
// tags exist so the boundary between the request and the instructions is
// unambiguous even when the JSON itself contains fences or braces.
func BuildPrompt(requestBody []byte) string {
	return buildPrompt(requestBody, "", nil)
}

// BuildRepairPrompt is that same envelope with the rejected reply and the
// complaint appended. A repair is not a different kind of conversation: the
// CLI sees the verbatim request again, so the correction cannot drift off the
// original task, and a backend that keeps no state between calls still has
// everything it needs.
func BuildRepairPrompt(requestBody []byte, bad string, cause error) string {
	return buildPrompt(requestBody, bad, cause)
}

func buildPrompt(requestBody []byte, bad string, cause error) string {
	var b strings.Builder
	b.WriteString("You are the model serving an OpenAI-compatible endpoint. Below is a real request to POST /v1/chat/completions, exactly as it arrived.\n\n")
	b.WriteString("<openai_request endpoint=\"/v1/chat/completions\">\n")
	b.Write(requestBody)
	b.WriteString("\n</openai_request>\n\n")

	b.WriteString(`<instructions>
Answer that request as the model. Reply with ONLY the OpenAI response object, inside <openai_response> tags, and no prose outside them:

<openai_response>
{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"your answer here"}}]}
</openai_response>

Rules:
- The CALLER executes the functions in the request's "tools" — you cannot run them yourself, and you must NOT use any tools of your own to do the task.
- To call functions, set finish_reason to "tool_calls" and put them in the message:
  {"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"NAME","arguments":{...}}}]}
- "content" is plain prose or null — NEVER structured data. Data a function takes goes in that call's "arguments". Describing a call in "content" does nothing; it is not a call.
- Put ALL independently runnable calls in one array. Never guess a value that must come from another call's result — make the prerequisite call first and wait for it.
- The request's "messages" already contain every call you have made and its result. Never repeat a call whose result is already there; use the result.
- Finish with a content-only message once the work is done.
</instructions>
`)

	if cause != nil {
		b.WriteString("\n<previous_attempt_rejected>\n")
		b.WriteString("Your previous reply to THIS request could not be parsed: " + cause.Error() + "\n\n")
		b.WriteString("It was:\n" + strings.TrimSpace(bad) + "\n\n")
		b.WriteString("Send the corrected <openai_response> now. Do not explain the correction.\n")
		b.WriteString("</previous_attempt_rejected>\n")
	}
	return b.String()
}

// responseTag pulls the body out of the <openai_response> envelope.
var responseTag = regexp.MustCompile(`(?s)<openai_response[^>]*>(.*?)</openai_response>`)

// fencedJSON is non-greedy on the fence but greedy on the object, so a reply
// whose JSON contains nested braces survives.
var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")

// ParseReply validates a CLI reply and turns it into the assistant message
// toolnexus expects. It accepts the <openai_response> envelope the contract
// asks for, a fenced or bare object, a whole chat.completion, a lone assistant
// message, and the legacy routsi kind/answer envelope. Anything else is
// ErrUnparseable — the caller retries or fails, and never invents an answer.
func ParseReply(text string) (toolnexus.InProcessResponse, error) {
	var none toolnexus.InProcessResponse

	// The contract asks for <openai_response> tags; a fenced block and a bare
	// object are accepted too, because models reach for those by habit.
	if m := responseTag.FindStringSubmatch(text); m != nil {
		text = m[1]
	}

	candidate := ""
	if m := fencedJSON.FindStringSubmatch(text); m != nil {
		candidate = m[1]
	} else if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		candidate = t
	}
	if candidate == "" {
		return none, fmt.Errorf("%w: no json object found — expected one inside <openai_response> tags", ErrUnparseable)
	}

	var obj map[string]any
	if e := json.Unmarshal([]byte(candidate), &obj); e != nil {
		return none, fmt.Errorf("%w: invalid json (%v)", ErrUnparseable, e)
	}

	// The full chat.completion shape: unwrap choices[0].message. The response's
	// own finish_reason is ignored — what the message CONTAINS decides, so a
	// model that says "stop" while emitting a call still gets it executed.
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
		return none, e
	}
	if len(calls) > 0 {
		return toolnexus.InProcessResponse{ToolCalls: calls}, nil
	}

	content, ok := obj["content"]
	if !ok {
		return none, fmt.Errorf("%w: object has neither content nor tool_calls", ErrUnparseable)
	}
	s, ok := content.(string)
	if !ok {
		if content == nil {
			return none, fmt.Errorf("%w: content is null and tool_calls is empty — say something or call something", ErrUnparseable)
		}
		// Structured data in content is the exact mistake the contract warns
		// about, so the complaint names it precisely and the model gets a
		// chance to move it into arguments.
		return none, fmt.Errorf("%w: content must be a plain string, got %T — structured data belongs in a tool call's arguments", ErrUnparseable, content)
	}
	if strings.TrimSpace(s) == "" {
		return none, fmt.Errorf("%w: content is empty and tool_calls is empty", ErrUnparseable)
	}
	return toolnexus.InProcessResponse{Content: s}, nil
}

// fromLegacyEnvelope handles routsi's {"kind":…,"answer":…} shape.
func fromLegacyEnvelope(obj map[string]any, kind string) (toolnexus.InProcessResponse, error) {
	var none toolnexus.InProcessResponse
	// tool_calls win over kind: models emit kind:"answer" alongside a populated
	// tool_calls array, and honouring kind there drops the call.
	calls, err := normalizeToolCalls(obj["tool_calls"])
	if err != nil {
		return none, err
	}
	if len(calls) > 0 {
		return toolnexus.InProcessResponse{ToolCalls: calls}, nil
	}
	if kind != "answer" {
		return none, fmt.Errorf("%w: kind=%q with no tool_calls", ErrUnparseable, kind)
	}
	s, ok := obj["answer"].(string)
	if !ok {
		return none, fmt.Errorf("%w: answer must be a plain string, got %T", ErrUnparseable, obj["answer"])
	}
	return toolnexus.InProcessResponse{Content: s}, nil
}

// normalizeToolCalls accepts the shapes models actually produce: the nested
// OpenAI {"function":{"name","arguments"}} form and the flat {"name","args"}
// form, with arguments as an object or as a JSON-encoded string. Encoding the
// arguments for the wire is toolnexus's job, so they are passed through as the
// value the model sent.
func normalizeToolCalls(v any) ([]toolnexus.InProcessToolCall, error) {
	if v == nil {
		return nil, nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: tool_calls must be an array, got %T", ErrUnparseable, v)
	}

	var out []toolnexus.InProcessToolCall
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
		id, _ := c["id"].(string)
		out = append(out, toolnexus.InProcessToolCall{ID: id, Name: name, Arguments: args})
	}
	return out, nil
}
