package agentadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// CodexServer keeps `codex app-server` running and talks its JSON-RPC:
// initialize once, then per turn thread/start (sandbox read-only, approval
// policy never) and turn/start, answered by the turn/completed notification.
//
// Measured on codex-cli 0.160.0 with a ChatGPT login and a bare "reply ok",
// turns 3 s apart: Reuse (one thread) ~2.1 s a turn; a fresh thread started
// ahead 2.65–3.25 s (each new thread runs codex's session-start hooks); a
// thread started inside the turn 4–6 s; a fresh `codex exec` ~6 s. So by default every turn gets a
// fresh thread (independent requests do not see each other) and the next
// thread is started as the turn ends. Reuse keeps one thread instead.
//
// This is codex's own server, not the npx codex-acp adapter: that one (0.16.0)
// bundles an older codex core that a ChatGPT login refuses every current
// model with.
type CodexServer struct {
	// Bin overrides "codex".
	Bin string
	// Model pins the model; "" ⇒ the turn's model, else codex's default.
	Model string
	// Sandbox is thread/start's sandbox; "" ⇒ "read-only".
	Sandbox string
	// Env is extra environment, "K=V".
	Env []string
	// Workdir is the threads' cwd; "" ⇒ the host's.
	Workdir string
	// Reuse keeps one thread across turns.
	Reuse bool
	// WarmPrompt, when set, is sent once by Warm as a throwaway turn.
	WarmPrompt string

	turn   sync.Mutex
	mu     sync.Mutex // guards the fields below
	conn   *codexConn
	bo     backoff
	spare  chan codexThread // one thread started ahead
	thread codexThread      // Reuse: the thread in use
	closed bool
}

type codexThread struct {
	conn  *codexConn
	id    string
	model string
}

// NewCodexServer returns a warm codex agent; call Warm at boot.
func NewCodexServer(c CLI) *CodexServer {
	return &CodexServer{Bin: c.Bin, Model: c.Model, Env: c.Env}
}

// Name implements Agent.
func (a *CodexServer) Name() string { return "codex" }

// Healthy implements WarmAgent.
func (a *CodexServer) Healthy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn != nil && a.conn.p.alive()
}

// Close implements WarmAgent.
func (a *CodexServer) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	if a.conn != nil {
		a.conn.p.kill()
		a.conn = nil
	}
	return nil
}

// Warm implements WarmAgent: the server, its handshake and a spare thread.
func (a *CodexServer) Warm(ctx context.Context) error {
	a.turn.Lock()
	defer a.turn.Unlock()
	if a.WarmPrompt != "" {
		_, err := a.run(ctx, a.Model, a.WarmPrompt)
		return err
	}
	c, err := a.ready(ctx)
	if err != nil {
		return err
	}
	th, err := a.takeThread(ctx, c, a.Model)
	if err != nil {
		return err
	}
	select {
	case a.spareSlot() <- th: // the next turn's thread
	default:
	}
	return nil
}

// Execute implements Agent.
func (a *CodexServer) Execute(ctx context.Context, t Turn) (string, error) {
	a.turn.Lock()
	defer a.turn.Unlock()
	model := a.Model
	if model == "" {
		model = t.Model
	}
	return a.run(ctx, model, t.Prompt)
}

func (a *CodexServer) spareSlot() chan codexThread {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spare == nil {
		a.spare = make(chan codexThread, 1)
	}
	return a.spare
}

func (a *CodexServer) run(ctx context.Context, model, prompt string) (string, error) {
	c, err := a.ready(ctx)
	if err != nil {
		return "", err
	}
	th, err := a.takeThread(ctx, c, model)
	if err == nil {
		var out string
		out, err = c.turn(ctx, th.id, prompt)
		if err == nil {
			a.mu.Lock()
			a.bo.ok()
			a.mu.Unlock()
			if !a.Reuse {
				go a.prepareSpare(c, model)
			}
			return out, nil
		}
	}
	// A dead server, a cancelled or failed turn: start over on the next turn.
	a.mu.Lock()
	defer a.mu.Unlock()
	if !c.p.alive() {
		a.bo.failed()
	}
	c.p.kill()
	if a.conn == c {
		a.conn = nil
	}
	a.thread = codexThread{}
	return "", err
}

// takeThread is the thread for this turn: the reused one, the spare, or a new one.
func (a *CodexServer) takeThread(ctx context.Context, c *codexConn, model string) (codexThread, error) {
	if a.Reuse && a.thread.conn == c && a.thread.model == model {
		return a.thread, nil
	}
	select {
	case th := <-a.spareSlot():
		if th.conn == c && th.model == model {
			return th, nil
		}
	default:
	}
	th, err := a.newThread(ctx, c, model)
	if err == nil && a.Reuse {
		a.thread = th
	}
	return th, err
}

func (a *CodexServer) prepareSpare(c *codexConn, model string) {
	th, err := a.newThread(context.Background(), c, model)
	if err != nil {
		return
	}
	select {
	case a.spareSlot() <- th:
	default:
	}
}

func (a *CodexServer) newThread(ctx context.Context, c *codexConn, model string) (codexThread, error) {
	sandbox := a.Sandbox
	if sandbox == "" {
		sandbox = "read-only"
	}
	cwd := a.Workdir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	params := map[string]any{"sandbox": sandbox, "approvalPolicy": "never", "cwd": cwd}
	if model != "" {
		params["model"] = model
	}
	res, err := c.call(ctx, "thread/start", params)
	if err != nil {
		return codexThread{}, err
	}
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &r); err != nil || r.Thread.ID == "" {
		return codexThread{}, fmt.Errorf("codex: thread/start returned no thread id")
	}
	return codexThread{conn: c, id: r.Thread.ID, model: model}, nil
}

// ready returns the running, initialized server, starting it if needed.
func (a *CodexServer) ready(ctx context.Context) (*codexConn, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, ErrClosed
	}
	if a.conn != nil && a.conn.p.alive() {
		c := a.conn
		a.mu.Unlock()
		return c, nil
	}
	if a.conn != nil {
		a.conn.p.kill()
		a.conn = nil
	}
	if err := a.bo.wait(ctx); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	bin := a.Bin
	if bin == "" {
		bin = "codex"
	}
	p, err := startProc(bin, []string{"app-server"}, a.Env, a.Workdir, "")
	if err != nil {
		a.bo.failed()
		a.mu.Unlock()
		return nil, err
	}
	c := newCodexConn(p)
	a.mu.Unlock()

	if _, err := c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "wfnexus-agentadapter", "version": "1"}}); err != nil {
		p.kill()
		a.mu.Lock()
		a.bo.failed()
		a.mu.Unlock()
		return nil, err
	}
	if err := c.send(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		p.kill()
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		p.kill()
		return nil, ErrClosed
	}
	a.conn = c
	return c, nil
}

// codexConn is one app-server process: requests matched to responses by id,
// notifications routed to the turn waiting on their thread.
type codexConn struct {
	p       *proc
	wmu     sync.Mutex
	next    atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan rpcMsg
	subs    map[string]chan rpcMsg
	dead    chan struct{}
}

type rpcMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func newCodexConn(p *proc) *codexConn {
	c := &codexConn{p: p, pending: map[int64]chan rpcMsg{}, subs: map[string]chan rpcMsg{}, dead: make(chan struct{})}
	go c.read()
	return c
}

func (c *codexConn) read() {
	defer close(c.dead)
	for line := range c.p.lines {
		var m rpcMsg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			// A request FROM codex (an approval, say). With approvalPolicy
			// never there should be none; refuse rather than leave it hanging.
			_ = c.send(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"error": map[string]any{"code": -32601, "message": "not supported by this client"}})
		case len(m.ID) > 0:
			id, err := strconv.ParseInt(string(m.ID), 10, 64)
			if err != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.Method != "":
			var p struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			c.mu.Lock()
			ch := c.subs[p.ThreadID]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				case <-c.p.quit:
				}
			}
		}
	}
}

func (c *codexConn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.p.write(b)
}

func (c *codexConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.next.Add(1)
	ch := make(chan rpcMsg, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, c.p.exitErr("codex")
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("codex: %s: %s", method, m.Error.Message)
		}
		return m.Result, nil
	case <-c.dead:
		return nil, c.p.exitErr("codex")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// turn runs one turn on thread and returns the agent's answer.
func (c *codexConn) turn(ctx context.Context, thread, prompt string) (string, error) {
	ch := make(chan rpcMsg, 1024)
	c.mu.Lock()
	c.subs[thread] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.subs, thread)
		c.mu.Unlock()
	}()
	if _, err := c.call(ctx, "turn/start", map[string]any{"threadId": thread,
		"input": []map[string]string{{"type": "text", "text": prompt}}}); err != nil {
		return "", err
	}
	var lastErr string
	for {
		select {
		case m := <-ch:
			switch m.Method {
			case "error":
				var p struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.Unmarshal(m.Params, &p) == nil && p.Error.Message != "" {
					lastErr = p.Error.Message
				}
			case "turn/completed":
				return codexAnswer(m.Params, lastErr)
			}
		case <-c.dead:
			return "", c.p.exitErr("codex")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func codexAnswer(params json.RawMessage, lastErr string) (string, error) {
	var p struct {
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
			Items []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"items"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("codex: turn/completed: %w", err)
	}
	if p.Turn.Status != "completed" {
		msg := lastErr
		if p.Turn.Error != nil && p.Turn.Error.Message != "" {
			msg = p.Turn.Error.Message
		}
		return "", fmt.Errorf("codex: turn %s: %s", p.Turn.Status, msg)
	}
	var parts []string
	for _, it := range p.Turn.Items {
		if it.Type == "agentMessage" && strings.TrimSpace(it.Text) != "" {
			parts = append(parts, strings.TrimSpace(it.Text))
		}
	}
	if len(parts) == 0 {
		return "", ErrEmptyTurn
	}
	return parts[len(parts)-1], nil
}

var _ WarmAgent = (*CodexServer)(nil)
