//go:build toolnexus_inprocess

// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag transport.go breaks `go build ./...` for the whole
// module, and the package is tagged as a unit so its tests keep compiling.
// Nothing is deleted:
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// Drop the tag once a toolnexus release carries the export.

// Package devinadapter makes a local agent CLI usable as a toolnexus model.
//
// It is one function in toolnexus terms — a Generate for
// toolnexus.CreateInProcessClient:
//
//	a := devinadapter.New(devinadapter.Options{
//	    Agent: devinadapter.Devin(devinadapter.CLI{Model: "SWE-1.6 Slow"}),
//	})
//	client := toolnexus.CreateInProcessClient(a.InProcessOptions())
//	res, err := client.Run(ctx, prompt, toolkit)
//
// InProcessOptions() is a plain value — set SystemPrompt, MaxTurns, Hooks or
// anything else on it before handing it over. The agent loop, MCP servers,
// skills, sub-agents, hooks and metrics are toolnexus's and are untouched: no
// transport, no chat.completion assembly and no usage bookkeeping is
// reimplemented here.
//
// What is left is the part that is genuinely this package's problem:
//
//   - the command seam — the Agent interface turns a rendered prompt file into
//     reply text. CommandAgent drives any CLI from an argv template, so devin,
//     claude and copilot are all just presets; anything else is an Agent you
//     write yourself (an SSH hop, a queue, a canned fixture).
//   - the contract — a one-shot CLI has no tool-call channel, so it is handed
//     the VERBATIM OpenAI request in an <openai_request> envelope and asked for
//     the OpenAI response back. The reply is validated, and a bad one goes back
//     with the complaint until the repair budget runs out.
//
// Skills need nothing extra: toolnexus injects the catalog into the system
// message and exposes `skill` as an ordinary tool, so both are already in the
// request the CLI receives.
package devinadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	toolnexus "github.com/muthuishere/toolnexus/golang"
)

// DefaultModelLabel is the model id reported when none was chosen. toolnexus
// requires a model name, but the CLI should fall back to its account default —
// so this exact label means "nobody chose", and is never passed down as a
// --model value. Any other name IS passed down.
const DefaultModelLabel = "cli-default"

// Options configures an Adapter.
type Options struct {
	// Agent is the backend that executes a turn. Nil ⇒ Devin(CLI{}), i.e.
	// `devin` from PATH. Swap it for any other CLI preset, a CommandAgent of
	// your own, or anything else implementing Agent.
	Agent Agent
	// Model pins the model, overriding what the caller asked for. "" ⇒ pass
	// through whatever toolnexus was configured with.
	Model string
	// Workdir is where prompt files are written. "" ⇒ the current directory.
	// A CommandAgent also runs its process here.
	Workdir string
	// Timeout bounds a single turn, repair attempts included. 0 ⇒ 10 minutes.
	Timeout time.Duration
	// Repairs is how many extra attempts a turn gets when the reply fails
	// validation: the offending output and the parse error go back to the
	// backend so it can correct itself. 0 ⇒ 2. Negative ⇒ no repair, the first
	// bad reply fails the run. It is never a silent fallback — once the budget
	// is spent the turn returns an error rather than a guess.
	Repairs int
	// KeepPromptFiles leaves the rendered prompt files on disk for inspection.
	KeepPromptFiles bool
	// Trace, when non-nil, receives every exchange — the prompt file path, the
	// prompt, and the raw reply. The hook for debugging and for tests.
	Trace func(Exchange)
}

// Exchange is one turn through the command seam, handed to Options.Trace.
type Exchange struct {
	Turn int
	// Attempt is 1 for the turn itself, 2+ for a repair attempt.
	Attempt    int
	PromptFile string
	Prompt     string
	Reply      string
	// ParseErr is set when the reply failed validation, which is what triggers
	// the next attempt.
	ParseErr error
	Err      error
}

// Adapter turns an Agent into a toolnexus in-process model.
//
// One Adapter is safe to share across any number of clients and concurrent
// runs: it holds no per-run state, and the only mutable field is an atomic turn
// counter. Whether the backend tolerates concurrent calls is the Agent's
// business — a CommandAgent spawns an independent process per turn, so it does.
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
	if opts.Repairs == 0 {
		opts.Repairs = 2
	}
	if opts.Repairs < 0 {
		opts.Repairs = 0
	}
	return &Adapter{opts: opts}
}

// InProcessOptions returns options wired to this adapter, ready for
// toolnexus.CreateInProcessClient. Fields not set here (SystemPrompt, MaxTurns,
// Hooks, OnMetric, …) are the caller's to fill in.
func (a *Adapter) InProcessOptions() toolnexus.InProcessOptions {
	model := a.opts.Model
	if model == "" {
		model = DefaultModelLabel
	}
	return toolnexus.InProcessOptions{
		Model:    model,
		Generate: a.Generate,
	}
}

// Generate is the model: one assembled request in, one assistant message out.
// It satisfies toolnexus.InProcessOptions.Generate and is usable on its own.
func (a *Adapter) Generate(req toolnexus.InProcessRequest) (toolnexus.InProcessResponse, error) {
	// The body is re-marshalled whole rather than picked apart, so everything
	// toolnexus assembled — messages, tools, tool_choice, response_format and
	// anything it starts sending later — reaches the CLI without this package
	// having to learn about it first.
	body, err := json.MarshalIndent(req.Body, "", "  ")
	if err != nil {
		return toolnexus.InProcessResponse{}, fmt.Errorf("devinadapter: encode request: %w", err)
	}

	// A pinned adapter wins; otherwise the caller's model goes down to the CLI,
	// unless it is the sentinel meaning nobody chose.
	model := a.opts.Model
	if model == "" && req.Model != DefaultModelLabel {
		model = req.Model
	}

	return a.call(context.Background(), body, model)
}

// call runs one turn: render to a file, invoke the backend, validate the reply.
// A reply that fails validation is sent back with the complaint, up to Repairs
// times; after that the turn errors out. Nothing is guessed.
func (a *Adapter) call(ctx context.Context, requestBody []byte, model string) (toolnexus.InProcessResponse, error) {
	turn := int(a.turn.Add(1))

	ctx, cancel := context.WithTimeout(ctx, a.opts.Timeout)
	defer cancel()

	var lastErr error
	prompt := BuildPrompt(requestBody)
	for attempt := 1; attempt <= a.opts.Repairs+1; attempt++ {
		text, err := a.invoke(ctx, turn, attempt, prompt, model, lastErr)
		if err != nil {
			return toolnexus.InProcessResponse{}, err
		}
		res, perr := ParseReply(text)
		if perr == nil {
			return res, nil
		}
		lastErr = perr
		prompt = BuildRepairPrompt(requestBody, text, perr)
	}
	return toolnexus.InProcessResponse{}, fmt.Errorf("devinadapter: %s gave no valid reply after %d attempts: %w",
		a.opts.Agent.Name(), a.opts.Repairs+1, lastErr)
}

// invoke writes the prompt to a file and hands it to the Agent.
func (a *Adapter) invoke(ctx context.Context, turn, attempt int, prompt, model string, prev error) (string, error) {
	ex := Exchange{Turn: turn, Attempt: attempt, Prompt: prompt, ParseErr: prev}

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

	text, err := a.opts.Agent.Execute(ctx, Turn{
		Index:      turn,
		Attempt:    attempt,
		PromptFile: ex.PromptFile,
		Prompt:     prompt,
		Model:      model,
		Workdir:    a.opts.Workdir,
	})
	ex.Reply, ex.Err = text, err
	if a.opts.Trace != nil {
		a.opts.Trace(ex)
	}
	return text, err
}
