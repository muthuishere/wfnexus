package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// THE MOCK PROVIDER — running a workflow for real, with no model.
//
// `wfx dryrun` answers "would this start": structure, budgets, env names, labels.
// It cannot answer the questions that actually go wrong — does a gate fire, do
// facts flow from one step to the next, can a step satisfy its own schema — and
// those need a RUN. A run needs a model, a model needs a key, and that is where
// somebody trying the product for the first time stops.
//
// So: a provider that produces turns and calls nothing. It reads the
// `submit_output` schema out of the request the agent loop already sends, builds
// a value that SATISFIES that schema, and submits it. The typed contract is
// enforced exactly as it is against a paid model — a step still cannot finish
// without a valid submit_output — so what a mock run proves is the wiring, and
// only the wiring.
//
// It is deliberately NOT a stub that returns `{}`. An empty object fails the
// schema, the step fails, and the mock would then only prove that the mock is
// broken.
//
// WHAT IT DOES NOT PROVE, and must never be mistaken for proving: whether a
// prompt gets a good answer. Every string it produces says so — "mock", never
// something that reads like a real finding — because the failure mode worth
// preventing is a demo whose output looks real.

// mockServer is the in-process endpoint a `mock` provider points at. One per
// engine, started on first use and never reachable from outside the machine.
type mockServer struct {
	once sync.Once
	url  string
	err  error
}

// URL starts the endpoint if it is not running and returns its base URL. It
// listens on 127.0.0.1 with an ephemeral port: this is not a service, it is a
// loopback answer to a question the agent loop asks over HTTP.
func (m *mockServer) URL() (string, error) {
	m.once.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			m.err = fmt.Errorf("mock provider: %w", err)
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(mockHandle)}
		go func() { _ = srv.Serve(ln) }()
		m.url = "http://" + ln.Addr().String()
	})
	return m.url, m.err
}

// mockHandle answers one chat completion.
//
// It is STATELESS, deciding what to say from the conversation it was handed
// rather than from a counter. A counter would be wrong the moment two steps ran
// in parallel — which the planner does by default — and wrong in a way that
// looks like a flaky model.
func mockHandle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Name    string `json:"name"`
			Content any    `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Already submitted? Then this is the wrap-up turn the loop asks for after a
	// tool result, and a reply with no tool calls closes the step.
	for _, msg := range req.Messages {
		if msg.Role == "tool" && strings.Contains(fmt.Sprint(msg.Content), "submit_output") {
			writeMockTurn(w, "", nil)
			return
		}
	}
	for _, t := range req.Tools {
		if t.Function.Name != "submit_output" {
			continue
		}
		writeMockTurn(w, "", map[string]any{
			"name": "submit_output",
			"args": mockValue(t.Function.Parameters, 0),
		})
		return
	}
	// No submit_output offered: a step that declares no schema. Plain text ends
	// it, which is what a real model would also do here.
	writeMockTurn(w, "mock: nothing to submit", nil)
}

func writeMockTurn(w http.ResponseWriter, text string, call map[string]any) {
	msg := map[string]any{"role": "assistant"}
	if call != nil {
		raw, _ := json.Marshal(call["args"])
		msg["content"] = nil
		msg["tool_calls"] = []any{map[string]any{
			"id": "mock_call", "type": "function",
			"function": map[string]any{"name": call["name"], "arguments": string(raw)},
		}}
	} else {
		if text == "" {
			text = "done"
		}
		msg["content"] = text
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "mock", "model": "mock",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}},
		// Zero, and zero is the truth: no tokens were bought. A mock that
		// reported plausible usage would put fiction in the cost column, which is
		// the one number people trust without checking.
		"usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	})
}

// maxMockDepth stops a self-referential schema — `$ref` to an ancestor, or a
// deeply nested object — from building a value forever.
const maxMockDepth = 12

// mockValue builds a value that satisfies a JSON schema.
//
// It honours the parts of a schema that decide whether a value VALIDATES:
// `required`, `enum`, `const`, `type`, `items`, `properties`, and the string and
// number bounds. It ignores the parts that only describe intent (`description`,
// `title`), because a value cannot fail on those.
//
// Every string is "mock" or "mock-<field>", so nobody can mistake a mock run's
// output for a real answer — which is the point where a convenience becomes a
// liability.
func mockValue(schema map[string]any, depth int) any {
	if schema == nil || depth > maxMockDepth {
		return nil
	}
	if c, ok := schema["const"]; ok {
		return c
	}
	if e, ok := schema["enum"].([]any); ok && len(e) > 0 {
		// The FIRST, always. A random pick would make a mock run irreproducible,
		// and the whole value of this is that two runs agree.
		return e[0]
	}
	switch mockType(schema) {
	case "object":
		out := map[string]any{}
		props, _ := schema["properties"].(map[string]any)
		// Required first, then every other declared property: a consumer reading
		// a fact this produced should see the shape it will really get, not the
		// minimum that validates.
		for _, name := range mockPropNames(props) {
			sub, _ := props[name].(map[string]any)
			out[name] = mockNamed(name, sub, depth+1)
		}
		// A required property with no declared schema still has to be present.
		for _, name := range mockRequired(schema) {
			if _, ok := out[name]; !ok {
				out[name] = "mock"
			}
		}
		return out
	case "array":
		items, _ := schema["items"].(map[string]any)
		n := 1
		if min, ok := mockNumber(schema["minItems"]); ok && int(min) > n {
			n = int(min)
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, mockValue(items, depth+1))
		}
		return out
	case "boolean":
		return true
	case "integer":
		return int(mockBounded(schema, 1))
	case "number":
		return mockBounded(schema, 1)
	case "null":
		return nil
	default:
		return mockString(schema, "mock")
	}
}

// mockNamed names the value after its field, so a fact reads as obviously
// synthetic wherever it surfaces — a log, the UI, a downstream prompt.
func mockNamed(name string, schema map[string]any, depth int) any {
	if schema == nil {
		return "mock-" + name
	}
	if mockType(schema) == "string" && schema["enum"] == nil && schema["const"] == nil {
		return mockString(schema, "mock-"+name)
	}
	return mockValue(schema, depth)
}

// mockString respects the bounds that would make a value invalid, and nothing
// else. A `format` is not honoured: a mock that tried to satisfy `date-time` and
// got it subtly wrong would be harder to debug than one that plainly did not.
func mockString(schema map[string]any, want string) string {
	if max, ok := mockNumber(schema["maxLength"]); ok && int(max) < len(want) {
		if int(max) <= 0 {
			return ""
		}
		want = want[:int(max)]
	}
	if min, ok := mockNumber(schema["minLength"]); ok {
		for len(want) < int(min) {
			want += "x"
		}
	}
	return want
}

func mockBounded(schema map[string]any, want float64) float64 {
	if min, ok := mockNumber(schema["minimum"]); ok && want < min {
		want = min
	}
	if max, ok := mockNumber(schema["maximum"]); ok && want > max {
		want = max
	}
	return want
}

func mockNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// mockType reads `type`, which may be a string or a list. A list picks the first
// concrete entry rather than "null", because a value of null satisfies the schema
// and tells a person nothing about the shape they will get.
func mockType(schema map[string]any) string {
	switch t := schema["type"].(type) {
	case string:
		return t
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s != "null" {
				return s
			}
		}
	}
	if _, ok := schema["properties"]; ok {
		return "object"
	}
	if _, ok := schema["items"]; ok {
		return "array"
	}
	return "string"
}

func mockRequired(schema map[string]any) []string {
	raw, _ := schema["required"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// mockPropNames is sorted, so the same schema produces the same value every time.
// Map order in Go is randomised, and a mock that varied between runs would make
// a failing test impossible to trust.
func mockPropNames(props map[string]any) []string {
	out := make([]string, 0, len(props))
	for name := range props {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
