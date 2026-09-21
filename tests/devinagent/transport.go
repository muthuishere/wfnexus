package devinadapter

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	toolnexus "github.com/muthuishere/toolnexus/golang"
)

// Transport exposes the same model as an http.RoundTripper, for the seams that
// take a transport instead of a Generate — notably agents.Options.Transport,
// which is how the sub-agent runtime is pointed at a model.
//
// It is a thin shim over Generate. toolnexus has this exact round tripper
// internally (inprocess.go) but does not export it, so the assembly below is
// duplicated rather than reused; it is deliberately the same shape, so a change
// upstream is easy to follow.
//
// Streaming is refused for the same reason toolnexus refuses it: a CLI returns
// a whole answer, and one chunk pretending to be many would pass a streaming
// assertion while being a lie.
func (a *Adapter) Transport() http.RoundTripper { return &adapterTransport{a: a} }

type adapterTransport struct{ a *Adapter }

func (t *adapterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &body)
	}
	if stream, _ := body["stream"].(bool); stream {
		return nil, errStreamUnsupported
	}

	msgs, _ := body["messages"].([]any)
	tools, _ := body["tools"].([]any)
	model, _ := body["model"].(string)

	answer, err := t.a.Generate(toolnexus.InProcessRequest{
		Messages: msgs, Tools: tools, Model: model, Body: body,
	})
	if err != nil {
		return nil, err
	}

	message := map[string]any{"role": "assistant"}
	finish := "stop"
	if len(answer.ToolCalls) > 0 {
		calls := make([]any, 0, len(answer.ToolCalls))
		for i, c := range answer.ToolCalls {
			id := c.ID
			if id == "" {
				id = "call_" + itoa(i)
			}
			calls = append(calls, map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": c.Name, "arguments": encodeArgs(c.Arguments)},
			})
		}
		message["tool_calls"] = calls
		finish = "tool_calls"
	} else {
		message["content"] = answer.Content
	}

	var prompt, completion, total int
	if answer.Usage != nil {
		prompt, completion, total = answer.Usage.PromptTokens, answer.Usage.CompletionTokens, answer.Usage.TotalTokens
	}
	if total == 0 {
		total = prompt + completion
	}

	out, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage": map[string]any{
			"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": total,
		},
	})
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(out)),
		Request:    req,
	}, nil
}

// AgentsLLM returns the LLM options the sub-agent runtime needs beside
// Transport: the sentinel base URL is never dialled, and the key is never used.
func (a *Adapter) AgentsLLM() (baseURL, apiKey, model string) {
	model = a.opts.Model
	if model == "" {
		model = DefaultModelLabel
	}
	return "http://in-process.invalid/v1", "in-process", model
}

func encodeArgs(v any) string {
	switch x := v.(type) {
	case nil:
		return "{}"
	case string:
		if x == "" {
			return "{}"
		}
		return x
	case json.RawMessage:
		return string(x)
	case []byte:
		return string(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

var errStreamUnsupported = streamError{}

type streamError struct{}

func (streamError) Error() string {
	return "devinadapter: streaming is not supported — a CLI returns a complete answer"
}
