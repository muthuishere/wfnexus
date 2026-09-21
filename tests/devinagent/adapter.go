// Package devinadapter makes a local agent CLI usable as a toolnexus model.
//
// There are two independent seams, and you can take either one:
//
//   - the HTTP seam — Adapter is an http.RoundTripper, so it drops into
//     toolnexus.ClientOptions.HTTPClient (or any other OpenAI-style client).
//     Nothing is dialed; the request is answered in process.
//   - the command seam — the Agent interface turns a rendered prompt file into
//     reply text. CommandAgent drives any CLI from an argv template, so devin,
//     claude and copilot are all just presets; anything else is an Agent you
//     write yourself (an SSH hop, a queue, a canned fixture).
//
// The adapter between them is pure translation: take what toolnexus assembled
// (system prompt, skills catalog, transcript, tool schemas), render it into a
// prompt FILE, hand that to the Agent, and translate the reply back into an
// OpenAI chat.completion — content or tool_calls.
//
//	a := devinadapter.New(devinadapter.Options{
//	    Agent: devinadapter.Devin(devinadapter.CLI{Model: "claude-sonnet-4"}),
//	})
//	agent := toolnexus.CreateClient(a.ClientOptions())
//	res, err := agent.Run(ctx, prompt, toolkit)
//
// ClientOptions() is a plain value — set SystemPrompt, MaxTurns, Hooks or
// anything else on it before handing it to CreateClient.
//
// Tool calls are emulated with the same fenced-JSON contract routsi uses
// (llm-forward-proxy/internal/backend/toolemu.go), because a one-shot CLI
// prints prose and has no native tool-call channel. Skills need nothing extra:
// toolnexus injects the catalog into the system prompt and exposes `skill` as
// an ordinary tool, so both ride the same path.
package devinadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	toolnexus "github.com/muthuishere/toolnexus/golang"
)

// DefaultBaseURL is never dialed — the RoundTripper answers every request in
// process. It exists because toolnexus builds an endpoint URL from it.
const DefaultBaseURL = "http://devin.local/v1"

// Options configures an Adapter.
type Options struct {
	// Agent is the backend that executes a turn. Nil ⇒ Devin(CLI{}), i.e.
	// `devin` from PATH. Swap it for any other CLI preset, a CommandAgent of
	// your own, or anything else implementing Agent.
	Agent Agent
	// Model is reported back in the completion. Purely cosmetic here — what the
	// CLI actually runs is the Agent's business. "" ⇒ echo what was requested.
	Model string
	// Workdir is where prompt files are written. "" ⇒ the current directory.
	// A CommandAgent also runs its process here.
	Workdir string
	// Timeout bounds a single turn. 0 ⇒ 10 minutes.
	Timeout time.Duration
	// KeepPromptFiles leaves the rendered prompt files on disk for inspection.
	KeepPromptFiles bool
	// Trace, when non-nil, receives every exchange — the prompt file path, the
	// prompt, and the raw reply. The hook for debugging and for tests.
	Trace func(Exchange)
}

// Exchange is one turn through the command seam, handed to Options.Trace.
type Exchange struct {
	Turn       int
	PromptFile string
	Prompt     string
	Reply      string
	Err        error
}

// Adapter is the http.RoundTripper that stands in for an OpenAI-style model.
//
// One Adapter is safe to share across any number of toolnexus clients and
// concurrent runs: it holds no per-run state, and the only mutable field is an
// atomic turn counter. Whether the backend itself tolerates concurrent calls
// is the Agent's business — a CommandAgent spawns an independent process per
// turn, so it does.
type Adapter struct {
	opts Options
	// turn counts invocations across the adapter's life, for traces.
	turn atomic.Int64
}

// New builds an Adapter from opts, applying the documented defaults.
func New(opts Options) *Adapter {
	if opts.Agent == nil {
		opts.Agent = Devin(CLI{})
	}
	if opts.Workdir == "" {
		opts.Workdir = "."
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}
	return &Adapter{opts: opts}
}

// HTTPClient returns an *http.Client whose transport is this adapter — the
// value to put in toolnexus.ClientOptions.HTTPClient, or in any other
// OpenAI-style SDK that takes an http client.
func (a *Adapter) HTTPClient() *http.Client {
	return &http.Client{Transport: a}
}

// ClientOptions returns toolnexus client options wired to this adapter, ready
// for toolnexus.CreateClient. Fields not set here (SystemPrompt, MaxTurns,
// Hooks, OnMetric, …) are the caller's to fill in.
//
// Retries default to 0: a failing local process is not a transient network
// blip, and re-running an agent CLI costs real time.
func (a *Adapter) ClientOptions() toolnexus.ClientOptions {
	model := a.opts.Model
	if model == "" {
		model = "devin"
	}
	return toolnexus.ClientOptions{
		BaseURL:    DefaultBaseURL,
		Style:      toolnexus.StyleOpenAI,
		Model:      model,
		APIKey:     "cli", // nothing leaves the machine; the transport is a process
		Retries:    0,
		HTTPClient: a.HTTPClient(),
	}
}

// RoundTrip implements http.RoundTripper.
func (a *Adapter) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	defer req.Body.Close()

	var in struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("devinadapter: decode request: %w", err)
	}

	prompt := RenderTranscript(in.Messages)
	if len(in.Tools) > 0 {
		prompt = BuildToolPrompt(prompt, in.Tools)
	}

	text, err := a.call(req.Context(), prompt)
	if err != nil {
		return nil, err
	}

	msg, finish := ParseToolReply(text)
	out, err := json.Marshal(map[string]any{
		"id":      "chatcmpl_" + randHex(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   a.modelName(in.Model),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		// The CLI reports no token usage. Keep the field present and honest
		// rather than inventing numbers.
		"usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	})
	if err != nil {
		return nil, err
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(out)),
		Request:    req,
	}, nil
}

func (a *Adapter) modelName(requested string) string {
	if a.opts.Model != "" {
		return a.opts.Model
	}
	return requested
}

// call writes the prompt to a file and hands it to the Agent.
func (a *Adapter) call(ctx context.Context, prompt string) (string, error) {
	turn := int(a.turn.Add(1))
	ex := Exchange{Turn: turn, Prompt: prompt}

	f, err := os.CreateTemp(a.opts.Workdir, "devin-prompt-*.md")
	if err != nil {
		return "", err
	}
	ex.PromptFile = f.Name()
	if !a.opts.KeepPromptFiles {
		defer os.Remove(ex.PromptFile)
	}
	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, a.opts.Timeout)
	defer cancel()

	text, err := a.opts.Agent.Execute(ctx, Turn{
		Index:      turn,
		PromptFile: ex.PromptFile,
		Prompt:     prompt,
		Workdir:    a.opts.Workdir,
	})
	ex.Reply, ex.Err = text, err
	if a.opts.Trace != nil {
		a.opts.Trace(ex)
	}
	return text, err
}
