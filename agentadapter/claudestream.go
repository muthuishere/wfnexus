package agentadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// ClaudeStreamArgs is the argv of the warm claude process: the one-shot
// preset's isolation (ClaudeArgs) with stream-json in and out, so one process
// takes message after message. No npx, no ACP adapter.
var ClaudeStreamArgs = []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
	"--tools", "", "--disable-slash-commands", "--strict-mcp-config", "--setting-sources", "", "--no-session-persistence"}

// ClaudeStream keeps `claude -p --input-format stream-json` running.
//
// Measured on claude 2.1.318 with a bare "reply ok": a process started ahead
// answers its first message in ~1.1 s; a cold start-and-answer is ~1.5–1.8 s
// and a fresh `claude -p` 2.6–3.5 s. One process keeps ONE conversation, so by
// default every turn uses a fresh process (a clean session) and the next one
// is started as the turn ends, to boot while the caller works. Reuse keeps one
// process and one conversation across turns instead.
type ClaudeStream struct {
	// Bin overrides "claude".
	Bin string
	// Model pins the model; "" ⇒ the turn's model, else the account default.
	Model string
	// ExtraArgs are appended to ClaudeStreamArgs.
	ExtraArgs []string
	// Env is extra environment, "K=V".
	Env []string
	// Workdir is the process's working directory; "" ⇒ the host's.
	Workdir string
	// Reuse keeps one conversation across turns.
	Reuse bool
	// WarmPrompt, when set, is sent once by Warm as a throwaway turn.
	WarmPrompt string

	turn   sync.Mutex // one turn at a time
	mu     sync.Mutex // guards the fields below
	cur    *proc
	bo     backoff
	closed bool
}

// NewClaudeStream returns a warm claude agent; call Warm at boot.
func NewClaudeStream(c CLI) *ClaudeStream {
	return &ClaudeStream{Bin: c.Bin, Model: c.Model, ExtraArgs: c.ExtraArgs, Env: c.Env}
}

// Name implements Agent.
func (a *ClaudeStream) Name() string { return "claude" }

// Healthy implements WarmAgent.
func (a *ClaudeStream) Healthy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cur != nil && a.cur.alive()
}

// Close implements WarmAgent.
func (a *ClaudeStream) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	if a.cur != nil {
		a.cur.kill()
		a.cur = nil
	}
	return nil
}

// Warm implements WarmAgent.
func (a *ClaudeStream) Warm(ctx context.Context) error {
	a.turn.Lock()
	defer a.turn.Unlock()
	if a.WarmPrompt != "" {
		_, err := a.run(ctx, a.Model, a.WarmPrompt)
		return err
	}
	_, err := a.ready(ctx, a.Model)
	return err
}

// Execute implements Agent.
func (a *ClaudeStream) Execute(ctx context.Context, t Turn) (string, error) {
	a.turn.Lock()
	defer a.turn.Unlock()
	model := a.Model
	if model == "" {
		model = t.Model
	}
	return a.run(ctx, model, t.Prompt)
}

func (a *ClaudeStream) bin() string {
	if a.Bin != "" {
		return a.Bin
	}
	return "claude"
}

// ready returns a running process for model, starting one if needed.
func (a *ClaudeStream) ready(ctx context.Context, model string) (*proc, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrClosed
	}
	if a.cur != nil && a.cur.alive() && a.cur.tag == model {
		return a.cur, nil
	}
	if a.cur != nil {
		a.cur.kill()
		a.cur = nil
	}
	if err := a.bo.wait(ctx); err != nil {
		return nil, err
	}
	p, err := a.start(model)
	if err != nil {
		a.bo.failed()
		return nil, err
	}
	a.cur = p
	return p, nil
}

func (a *ClaudeStream) start(model string) (*proc, error) {
	args := append([]string{}, ClaudeStreamArgs...)
	if model != "" {
		args = append(args, "--model", model)
	}
	return startProc(a.bin(), append(args, a.ExtraArgs...), a.Env, a.Workdir, model)
}

func (a *ClaudeStream) run(ctx context.Context, model, prompt string) (string, error) {
	p, err := a.ready(ctx, model)
	if err != nil {
		return "", err
	}
	out, err := claudeTurn(ctx, p, prompt)

	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.bo.ok()
	} else if !p.alive() {
		a.bo.failed()
	}
	if err != nil || !a.Reuse {
		// A failed turn leaves the conversation in an unknown state, and a
		// fresh-per-turn agent is done with this one: replace it now so the
		// next process boots while the caller works.
		p.kill()
		a.cur = nil
		if !a.closed && a.bo.fails == 0 {
			if next, serr := a.start(model); serr == nil {
				a.cur = next
			}
		}
	}
	return out, err
}

// claudeTurn writes one user message and reads to its result event.
func claudeTurn(ctx context.Context, p *proc, prompt string) (string, error) {
	msg, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": prompt}}},
	})
	if err := p.write(msg); err != nil {
		return "", p.exitErr("claude")
	}
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				return "", p.exitErr("claude")
			}
			var ev struct {
				Type    string `json:"type"`
				Subtype string `json:"subtype"`
				IsError bool   `json:"is_error"`
				Result  string `json:"result"`
			}
			if json.Unmarshal(line, &ev) != nil || ev.Type != "result" {
				continue
			}
			if ev.IsError || ev.Subtype != "success" {
				return "", fmt.Errorf("claude: %s: %s", ev.Subtype, strings.TrimSpace(ev.Result))
			}
			out := strings.TrimSpace(ev.Result)
			if out == "" {
				return "", ErrEmptyTurn
			}
			return out, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

var _ WarmAgent = (*ClaudeStream)(nil)
