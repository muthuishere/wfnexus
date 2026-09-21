package devinadapter

import "context"

// Agent is the command seam — the one interface every backend implements.
//
// Everything above it (the OpenAI translation, tool-call emulation, skills,
// the toolnexus loop) is written against this and nothing else, so adding a
// backend is writing one method. CommandAgent already covers "a CLI that takes
// a prompt file and prints an answer", which is most of them; implement Agent
// directly when the backend is not a local process — an SSH hop, a queue, a
// canned fixture in a test.
type Agent interface {
	// Name identifies the backend in traces and errors, e.g. "devin".
	Name() string
	// Execute runs one turn and returns the reply text. The prompt is already
	// rendered and written to t.PromptFile; an implementation may read the
	// file, use t.Prompt directly, or ignore both.
	Execute(ctx context.Context, t Turn) (string, error)
}

// Turn is one request through the command seam.
type Turn struct {
	// Index is the 1-based turn number within this adapter's life. Turn 2+ is
	// the loop coming back with tool results folded into the transcript.
	Index int
	// PromptFile is the rendered prompt on disk. It is removed after the turn
	// unless Options.KeepPromptFiles is set.
	PromptFile string
	// Prompt is the same text, in memory.
	Prompt string
	// Workdir is Options.Workdir, resolved.
	Workdir string
}

// AgentFunc adapts a plain function to Agent.
type AgentFunc struct {
	Label string
	Fn    func(ctx context.Context, t Turn) (string, error)
}

func (f AgentFunc) Name() string {
	if f.Label == "" {
		return "func"
	}
	return f.Label
}

func (f AgentFunc) Execute(ctx context.Context, t Turn) (string, error) { return f.Fn(ctx, t) }

// compile-time checks that the shipped backends satisfy the interface.
var (
	_ Agent = (*CommandAgent)(nil)
	_ Agent = AgentFunc{}
)
