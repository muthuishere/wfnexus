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
	"os"
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

// ACPPreset is how one agent speaks ACP: the program, its argv, how it is put
// on a model, and the session mode that runs without stopping to ask.
type ACPPreset struct {
	Bin  string
	Argv []string
	// ModelFlag, when set, puts the model on the command line before Argv
	// (devin's global --model). "" ⇒ the model is chosen over the protocol.
	ModelFlag string
	// Mode is the agent's own id for "act without asking".
	Mode string
	// Install is how a person gets the program, shown when it is missing.
	Install string
	// Env is added to the agent's environment before the step's own env.
	Env []string
}

// opencodeAsAModel denies every tool opencode has of its own. wfnexus drives an
// ACP agent as a MODEL: the step's tools are called by wfnexus and handed back
// in the transcript. Left to itself opencode's build agent ran its own bash
// instead and a step sat for 20+ minutes with no turn finished (2026-09-27).
// Measured: a per-tool "deny" removes the tool from the model; `tools: false`
// was ignored and a "*" deny made it answer nothing.
var opencodeAsAModel = `OPENCODE_CONFIG_CONTENT={"permission":{"bash":"deny","edit":"deny","write":"deny","patch":"deny","read":"deny","glob":"deny","grep":"deny","list":"deny","webfetch":"deny","websearch":"deny","codesearch":"deny","task":"deny","todowrite":"deny","todoread":"deny","skill":"deny","question":"deny"}}`

// ACPPresets are the agents wfnexus knows by name. Each was measured over real
// stdio ACP (2026-09-27): all three advertise their models as a "model"
// configOption at session/new.
var ACPPresets = map[string]ACPPreset{
	// devin advertises only its current model over ACP; the others are reached
	// with its --model flag, so the flag stays.
	"devin": {Bin: "devin", Argv: []string{"acp"}, ModelFlag: "--model", Mode: ACPModeBypass,
		Install: "curl -fsSL https://cli.devin.ai/install.sh | bash"},
	// opencode offers every provider it is configured for (~680), free ones included.
	"opencode": {Bin: "opencode", Argv: []string{"acp"}, Mode: "build",
		Install: "brew install sst/tap/opencode", Env: []string{opencodeAsAModel}},
	// codex speaks ACP through Zed's adapter; it uses codex's own login.
	"codex": {Bin: "npx", Argv: []string{"-y", "@zed-industries/codex-acp"}, Mode: "full-access",
		Install: "npm i -g @openai/codex && codex login"},
}

// ACP configures an ACPAgent.
type ACP struct {
	// Bin overrides the executable. "" ⇒ "devin".
	Bin string
	// Model is the model the session runs on. "" ⇒ the agent's own default.
	// With ModelFlag it goes on the command line; otherwise it is chosen over
	// the protocol after session/new and must be one the agent advertised, so
	// a typo fails at start instead of running quietly on the default model.
	Model string
	// Argv is the agent's arguments (["acp"] for devin and opencode). nil ⇒
	// the devin preset's.
	Argv []string
	// ModelFlag puts Model on the command line instead of choosing it over the
	// protocol (devin's --model). "" ⇒ protocol.
	ModelFlag string
	// Env is appended to the agent's environment (which otherwise inherits
	// this process's).
	Env []string
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
	// AllowNativeTools answers the agent's session/request_permission with
	// ALLOW. Off by default: the platform's loop executes the tools, and the
	// step's allowlist and guardrails exist only there. An agent's own shell
	// or editor runs outside them, so by default its request is refused (the
	// agent's reject option, or a cancelled outcome when it offers none).
	AllowNativeTools bool
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

	// procMu guards proc on its own, so Close can kill the process while a
	// turn holds mu — which is exactly when it needs to.
	procMu sync.Mutex
	proc   *os.Process

	// models is what the agent advertised at session/new; current is the
	// model the session is on after start.
	models  []ACPModel
	current string
}

// ACPModel is one model an ACP agent offers.
type ACPModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Free is true only when the id says so (opencode's "-free", OpenRouter's
	// ":free"). Never guessed from price or name.
	Free bool `json:"free"`
}

func isFree(id string) bool { return strings.HasSuffix(id, "-free") || strings.HasSuffix(id, ":free") }

// NewACP builds an ACPAgent. Nothing starts until the first Execute.
func NewACP(cfg ACP) *ACPAgent {
	// No Argv means devin's way of speaking ACP (the adapter's original and
	// default agent): `devin [--model m] acp`, whatever Bin says.
	if d := ACPPresets["devin"]; cfg.Argv == nil {
		cfg.Argv, cfg.ModelFlag = d.Argv, d.ModelFlag
		if cfg.Bin == "" {
			cfg.Bin = d.Bin
		}
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
	// Kill first, WITHOUT mu: a turn blocked in session/prompt holds mu, and
	// waiting for it is what kept a cancelled step alive. The dead process
	// closes stdout, the reader reports it, and the blocked call returns.
	a.procMu.Lock()
	if a.proc != nil {
		_ = a.proc.Kill()
		a.proc = nil
	}
	a.procMu.Unlock()

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

	var args []string
	if a.cfg.ModelFlag != "" && a.cfg.Model != "" {
		args = append(args, a.cfg.ModelFlag, a.cfg.Model) // a global flag, before the subcommand
	}
	args = append(append(args, a.cfg.Argv...), a.cfg.ExtraArgs...)

	// Deliberately NOT CommandContext: the process must outlive the turn that
	// happened to start it. Close owns its lifetime.
	cmd := exec.Command(a.cfg.Bin, args...)
	cwd := a.cfg.Cwd
	if cwd == "" {
		cwd = workdir
	}
	cmd.Dir = cwd
	if len(a.cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), a.cfg.Env...)
	}

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
	a.procMu.Lock()
	a.proc = cmd.Process
	a.procMu.Unlock()
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
	a.models, a.current = parseModels(raw)

	if a.cfg.ModelFlag == "" && a.cfg.Model != "" && a.cfg.Model != a.current {
		if err := a.selectModel(startCtx); err != nil {
			a.started = false
			return err
		}
	}

	// A failure here is not fatal: the mode is a convenience, and a build
	// without set_mode still works — it just may stop to ask.
	_, _ = a.call(startCtx, "session/set_mode", map[string]any{
		"sessionId": a.session, "modeId": a.cfg.Mode,
	})
	return nil
}

// selectModel puts the session on cfg.Model over the protocol:
// session/set_config_option (the "model" option every measured agent
// advertises), falling back to the older session/set_model. When the agent
// answers with its options, the model it reports is checked, so a model the
// agent quietly ignored is an error, not a run on the wrong model.
func (a *ACPAgent) selectModel(ctx context.Context) error {
	want := a.cfg.Model
	if len(a.models) > 0 && !hasModel(a.models, want) {
		return fmt.Errorf("acp: the agent does not offer model %q (it offers %d; `wfx models <provider>` lists them)", want, len(a.models))
	}
	raw, err := a.call(ctx, "session/set_config_option", map[string]any{
		"sessionId": a.session, "configId": "model", "value": want,
	})
	if err != nil {
		if _, err2 := a.call(ctx, "session/set_model", map[string]any{
			"sessionId": a.session, "modelId": want,
		}); err2 != nil {
			return fmt.Errorf("acp: could not select model %q: %v; %v", want, err, err2)
		}
		a.current = want
		return nil
	}
	if _, cur := parseModels(raw); cur != "" && cur != want {
		return fmt.Errorf("acp: asked for model %q, the agent is on %q", want, cur)
	}
	a.current = want
	return nil
}

// Models starts the agent if needed and returns the models it offers and the
// one this session is on. An agent that advertises none returns an empty list.
func (a *ACPAgent) Models(ctx context.Context, workdir string) ([]ACPModel, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.start(ctx, workdir); err != nil {
		return nil, "", err
	}
	return a.models, a.current, nil
}

// parseModels reads both shapes agents use: configOptions (a "model" select,
// possibly grouped — opencode) and the older models{availableModels,
// currentModelId}.
func parseModels(raw json.RawMessage) ([]ACPModel, string) {
	var r struct {
		ConfigOptions []struct {
			ID           string          `json:"id"`
			Category     string          `json:"category"`
			CurrentValue string          `json:"currentValue"`
			Options      json.RawMessage `json:"options"`
		} `json:"configOptions"`
		Models *struct {
			CurrentModelID  string `json:"currentModelId"`
			AvailableModels []struct {
				ModelID string `json:"modelId"`
				Name    string `json:"name"`
			} `json:"availableModels"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil, ""
	}
	for _, o := range r.ConfigOptions {
		if o.Category != "model" && o.ID != "model" {
			continue
		}
		type opt struct {
			Value   string `json:"value"`
			Name    string `json:"name"`
			Options []opt  `json:"options"`
		}
		var opts []opt
		_ = json.Unmarshal(o.Options, &opts)
		var out []ACPModel
		var walk func([]opt)
		walk = func(os []opt) {
			for _, x := range os {
				if x.Value != "" {
					out = append(out, ACPModel{ID: x.Value, Name: x.Name, Free: isFree(x.Value)})
				}
				walk(x.Options)
			}
		}
		walk(opts)
		return out, o.CurrentValue
	}
	if r.Models != nil {
		var out []ACPModel
		for _, m := range r.Models.AvailableModels {
			out = append(out, ACPModel{ID: m.ModelID, Name: m.Name, Free: isFree(m.ModelID)})
		}
		return out, r.Models.CurrentModelID
	}
	return nil, ""
}

func hasModel(ms []ACPModel, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
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
				Code    int             `json:"code"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		if env.Error != nil {
			// "Internal error" alone hides the cause; agents put it in data
			// (codex: "The 'gpt-5.4-mini' model is not supported when using
			// Codex with a ChatGPT account").
			detail := ""
			if len(env.Error.Data) > 0 && string(env.Error.Data) != "null" {
				var d struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(env.Error.Data, &d) == nil && d.Message != "" {
					detail = ": " + d.Message
				} else {
					detail = ": " + string(env.Error.Data)
				}
			}
			return nil, fmt.Errorf("acp: %s: %s (code %d)%s", method, env.Error.Message, env.Error.Code, detail)
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
			// Always answered — a silent hang would cost a whole turn (gate 2).
			// But REFUSED unless native tools were opted into: the platform's
			// loop owns the tools, and it is there that the step's allowlist and
			// guardrails are enforced. An agent that runs its own shell instead
			// (opencode's `build` agent does, dozens of commands a turn) acts
			// outside every one of them. Refused, it is left with the protocol:
			// a tool call in its reply, which the platform executes.
			a.answerPermission(*msg.ID, msg.Params, a.cfg.AllowNativeTools)
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
func (a *ACPAgent) answerPermission(id int, params json.RawMessage, allow bool) {
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(params, &p)

	want := "reject"
	if allow {
		want = "allow"
	}
	choice := ""
	for _, o := range p.Options {
		if strings.HasPrefix(o.Kind, want) {
			choice = o.OptionID
			break
		}
	}
	outcome := map[string]any{"outcome": "selected", "optionId": choice}
	switch {
	case choice == "" && allow && len(p.Options) > 0:
		outcome["optionId"] = p.Options[0].OptionID
	case choice == "":
		// No reject option offered: ACP's other refusal is a cancelled
		// request — never the first option, which is usually "allow".
		outcome = map[string]any{"outcome": "cancelled"}
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"result":  map[string]any{"outcome": outcome},
	})
	if err != nil {
		return
	}
	_, _ = a.stdin.Write(append(body, '\n'))
}
