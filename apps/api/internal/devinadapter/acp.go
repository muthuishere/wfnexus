// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag transport.go breaks `go build ./...` for the whole
// module, and the package is tagged as a unit so its tests keep compiling.
// Nothing is deleted:
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// Drop the tag once a toolnexus release carries the export.

package devinadapter

// ACPAgent — the fast backend. One long-lived `devin acp` process instead of a
// fresh `devin -p` per turn.
//
// Measured on this machine (SWE-1.6 Slow), trivial prompts:
//
//	devin -p        17 bytes  15.3s   ← every turn pays this
//	devin -p        12 KB     14.3s   ← prompt size is free; startup is not
//	acp initialize             0.02s
//	acp session/new            3.0s   ← once
//	acp prompt #1             17.2s   ← once (model warm-up)
//	acp prompt #2              5.2s
//	acp prompt #3              1.6s
//
// So the per-turn cost is process startup, not tokens, and a four-turn agent
// loop goes from ~60s of pure overhead to roughly one startup. That is the
// whole reason this file exists.
//
// The protocol is ACP (Agent Client Protocol) — JSON-RPC 2.0, one object per
// line, over the child's stdin/stdout. Only the four methods an agent loop
// needs are implemented; everything else the agent sends is ignored on purpose.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ACPMode is a devin session mode. Bypass is the default here for the same
// reason PermissionBypass is: nothing is driving this interactively, so a
// permission prompt would simply hang until the turn times out.
const (
	ACPModeBypass      = "bypass"
	ACPModeAcceptEdits = "accept-edits"
	ACPModeAsk         = "ask"
	ACPModePlan        = "plan"
)

// ACP configures an ACPAgent.
type ACP struct {
	// Bin overrides the executable. "" ⇒ "devin".
	Bin string
	// Model is passed as a global --model before the acp subcommand. "" ⇒ the
	// account default. Unlike the one-shot CLI this is fixed for the life of
	// the process, since the session outlives a turn.
	Model string
	// Cwd is the session's working directory. "" ⇒ the turn's Workdir.
	Cwd string
	// Mode is the session mode. "" ⇒ ACPModeBypass.
	Mode string
	// StartTimeout bounds initialize + session/new. 0 ⇒ 2 minutes.
	StartTimeout time.Duration
	// ExtraArgs are appended after the acp subcommand.
	ExtraArgs []string
	// NoSupersede drops the "this supersedes everything earlier" marker that
	// turn 2+ normally carries. The marker exists because toolnexus assembles
	// a COMPLETE request every turn while an ACP session is stateful, so the
	// agent sees near-duplicate histories. Whether it is load-bearing or just
	// a precaution is an open question (toolnexus ADR 0025 gate 1) — this flag
	// is how it gets measured rather than assumed.
	NoSupersede bool
	// NoAnswerPermission stops the client answering session/request_permission.
	// An unanswered request means the agent waits forever and the turn dies at
	// the timeout — the trap ADR 0025 gate 2 asks to demonstrate. Never set
	// this outside an experiment.
	NoAnswerPermission bool
}

// ACPAgent drives one persistent devin process. It is an Agent, so it drops
// into Options.Agent wherever Devin(CLI{}) would go.
//
// The process starts on the first Execute and lives until Close. Turns are
// serialized: one ACP session is one conversation, so two concurrent prompts
// would interleave into the same transcript. A caller wanting real parallelism
// runs one ACPAgent (hence one process) per lane.
type ACPAgent struct {
	cfg ACP

	mu      sync.Mutex // serializes turns AND guards the fields below
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	session string
	nextID  int
	started bool

	// pending routes a JSON-RPC response back to the caller waiting on it.
	pendingMu sync.Mutex
	pending   map[int]chan json.RawMessage

	// chunks accumulates the current turn's assistant text, appended by the
	// reader goroutine as session/update notifications arrive.
	chunksMu sync.Mutex
	chunks   strings.Builder

	readErr chan error
}

// NewACP builds an ACPAgent. Nothing starts until the first Execute.
func NewACP(cfg ACP) *ACPAgent {
	if cfg.Bin == "" {
		cfg.Bin = "devin"
	}
	if cfg.Mode == "" {
		cfg.Mode = ACPModeBypass
	}
	if cfg.StartTimeout == 0 {
		cfg.StartTimeout = 2 * time.Minute
	}
	return &ACPAgent{cfg: cfg, pending: map[int]chan json.RawMessage{}, readErr: make(chan error, 1)}
}

// Name implements Agent.
func (a *ACPAgent) Name() string { return "devin-acp" }

// Execute implements Agent: one prompt into the live session, the assistant's
// text back. The prompt file is written by the adapter either way — it is what
// Trace and KeepPromptFiles expose — but ACP takes the text inline.
func (a *ACPAgent) Execute(ctx context.Context, t Turn) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.start(ctx, t.Workdir); err != nil {
		return "", err
	}

	a.chunksMu.Lock()
	a.chunks.Reset()
	a.chunksMu.Unlock()

	prompt := t.Prompt
	if (t.Index > 1 || t.Attempt > 1) && !a.cfg.NoSupersede {
		// The session remembers the previous turns, and each of our prompts is
		// a COMPLETE request rather than a delta — so the agent is looking at
		// what appears to be the same question again with one more message on
		// the end. This names which copy is live.
		prompt = "The request below SUPERSEDES everything earlier in this conversation. Answer this one.\n\n" + prompt
	}

	if _, err := a.call(ctx, "session/prompt", map[string]any{
		"sessionId": a.session,
		"prompt":    []any{map[string]any{"type": "text", "text": prompt}},
	}); err != nil {
		return "", err
	}

	a.chunksMu.Lock()
	out := a.chunks.String()
	a.chunksMu.Unlock()

	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("devin-acp: the session produced no text")
	}
	return strings.TrimSpace(out), nil
}

// Close stops the process. Safe to call more than once, and safe to call on an
// agent that never started.
func (a *ACPAgent) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		return nil
	}
	a.started = false
	if a.stdin != nil {
		_ = a.stdin.Close()
	}
	if a.cmd != nil && a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
		_ = a.cmd.Wait()
	}
	return nil
}

// start boots the process and opens a session, once.
func (a *ACPAgent) start(ctx context.Context, workdir string) error {
	if a.started {
		return nil
	}

	args := []string{}
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model) // global flag, before the subcommand
	}
	args = append(args, "acp")
	args = append(args, a.cfg.ExtraArgs...)

	// Deliberately NOT CommandContext: the process must outlive the turn that
	// happened to start it. Close owns its lifetime.
	cmd := exec.Command(a.cfg.Bin, args...)
	cwd := a.cfg.Cwd
	if cwd == "" {
		cwd = workdir
	}
	cmd.Dir = cwd

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// The agent logs to stderr; draining it keeps the pipe from filling and
	// blocking the child.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("devin-acp: start: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	a.cmd, a.stdin, a.started = cmd, stdin, true
	go a.read(stdout)

	startCtx, cancel := context.WithTimeout(ctx, a.cfg.StartTimeout)
	defer cancel()

	if _, err := a.call(startCtx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			// This client does not serve files back to the agent; it is a model
			// host, not an editor.
			"fs": map[string]any{"readTextFile": false, "writeTextFile": false},
		},
	}); err != nil {
		a.started = false
		return fmt.Errorf("devin-acp: initialize: %w", err)
	}

	raw, err := a.call(startCtx, "session/new", map[string]any{
		"cwd": cwd, "mcpServers": []any{},
	})
	if err != nil {
		a.started = false
		return fmt.Errorf("devin-acp: session/new: %w", err)
	}
	var sess struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &sess); err != nil || sess.SessionID == "" {
		a.started = false
		return fmt.Errorf("devin-acp: session/new returned no session id")
	}
	a.session = sess.SessionID

	// A failure here is not fatal: the mode is a convenience, and a build
	// without set_mode still works — it just may stop to ask.
	_, _ = a.call(startCtx, "session/set_mode", map[string]any{
		"sessionId": a.session, "modeId": a.cfg.Mode,
	})
	return nil
}

// call sends a request and waits for its response.
func (a *ACPAgent) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	a.nextID++
	id := a.nextID
	ch := make(chan json.RawMessage, 1)

	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()
	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
	}()

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	if _, err := a.stdin.Write(append(body, '\n')); err != nil {
		return nil, fmt.Errorf("devin-acp: write %s: %w", method, err)
	}

	select {
	case raw := <-ch:
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		if env.Error != nil {
			return nil, fmt.Errorf("devin-acp: %s: %s (code %d)", method, env.Error.Message, env.Error.Code)
		}
		return env.Result, nil
	case err := <-a.readErr:
		return nil, fmt.Errorf("devin-acp: %s: %w", method, err)
	case <-ctx.Done():
		return nil, fmt.Errorf("devin-acp: %s: %w", method, ctx.Err())
	}
}

// read is the demultiplexer: responses go to whoever is waiting, assistant
// text is accumulated, permission requests are answered, everything else is
// ignored.
func (a *ACPAgent) read(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // a turn's text can be large

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &msg) != nil {
			continue
		}

		switch {
		case msg.ID != nil && msg.Method == "":
			// A response to one of our calls.
			raw := make(json.RawMessage, len(line))
			copy(raw, line)
			a.pendingMu.Lock()
			ch := a.pending[*msg.ID]
			a.pendingMu.Unlock()
			if ch != nil {
				ch <- raw
			}

		case msg.Method == "session/update":
			a.onUpdate(msg.Params)

		case msg.ID != nil && strings.Contains(msg.Method, "permission") && a.cfg.NoAnswerPermission:
			// Deliberately ignored: the turn will now hang (gate 2).

		case msg.ID != nil && strings.Contains(msg.Method, "permission"):
			// Bypass mode should mean this never fires; answering anyway costs
			// nothing and a silent hang would cost a whole turn.
			a.allow(*msg.ID, msg.Params)
		}
	}
	err := sc.Err()
	if err == nil {
		err = errors.New("the agent process closed its output")
	}
	select {
	case a.readErr <- err:
	default:
	}
}

func (a *ACPAgent) onUpdate(params json.RawMessage) {
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	// Only the assistant's own message is the reply. Thought chunks and tool
	// traffic are the agent narrating itself, and folding those in would put
	// prose around the JSON the contract asks for.
	if p.Update.SessionUpdate != "agent_message_chunk" || p.Update.Content.Text == "" {
		return
	}
	a.chunksMu.Lock()
	a.chunks.WriteString(p.Update.Content.Text)
	a.chunksMu.Unlock()
}

// allow answers a permission request with the first allow-shaped option.
func (a *ACPAgent) allow(id int, params json.RawMessage) {
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(params, &p)

	choice := ""
	for _, o := range p.Options {
		if strings.HasPrefix(o.Kind, "allow") {
			choice = o.OptionID
			break
		}
	}
	if choice == "" && len(p.Options) > 0 {
		choice = p.Options[0].OptionID
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"result": map[string]any{
			"outcome": map[string]any{"outcome": "selected", "optionId": choice},
		},
	})
	if err != nil {
		return
	}
	_, _ = a.stdin.Write(append(body, '\n'))
}
