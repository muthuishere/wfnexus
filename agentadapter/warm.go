package agentadapter

// Warm agents: a CLI kept running by the host, ready before the first request.
//
// CommandAgent pays a process start on every turn (claude -p 2.6–3.5 s, codex
// exec ~6 s, measured in issue #68). The agents in this file start the CLI's
// own long-lived mode once and feed it requests:
//
//	ClaudeStream  claude -p --input-format stream-json    ~1.1 s a turn
//	CodexServer   codex app-server (JSON-RPC)             ~2.5 s a turn
//
// Both give every turn a clean session by default, prepared AHEAD of the turn
// (a spare claude process, a spare codex thread), so independent requests do
// not see each other and do not wait for the session to start. A dead process
// is restarted on the next turn, with backoff, and its stderr tail is kept for
// the error. A host calls Warm at boot; Pool shares several among callers.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// WarmAgent is an Agent backed by a long-lived process the host owns.
type WarmAgent interface {
	Agent
	// Warm starts the process (and its first session) now, so the first real
	// turn does not pay for it. Safe to call again; it is a no-op when warm.
	Warm(ctx context.Context) error
	// Healthy reports whether the process is running right now. A turn on an
	// unhealthy agent restarts it first.
	Healthy() bool
	// Close stops the process. Execute after Close fails.
	Close() error
}

// ErrClosed is returned by a warm agent used after Close.
var ErrClosed = errors.New("agentadapter: agent is closed")

// proc is one long-lived child that speaks newline-delimited JSON on stdio.
type proc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan []byte   // stdout, one line each; closed at EOF
	done  chan struct{} // closed once the process has exited
	quit  chan struct{} // closed by kill: stop delivering lines
	once  sync.Once
	err   error       // the exit, valid after done
	tail  *tailBuffer // the end of stderr
	tag   string      // what the process was started for (the model)
}

func startProc(bin string, args, env []string, dir, tag string) (*proc, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &proc{cmd: cmd, stdin: in, lines: make(chan []byte, 64), done: make(chan struct{}), quit: make(chan struct{}), tail: &tailBuffer{}, tag: tag}
	cmd.Stderr = p.tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			select {
			case p.lines <- append([]byte(nil), sc.Bytes()...):
			case <-p.quit: // nobody reads a killed process; drain to EOF
			}
		}
		close(p.lines)
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *proc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *proc) write(b []byte) error {
	_, err := p.stdin.Write(append(b, '\n'))
	return err
}

func (p *proc) kill() {
	p.once.Do(func() { close(p.quit) })
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// exitErr describes a process that stopped answering: its exit and the end of
// its stderr, which is where a CLI says why (a login, a bad flag, a crash).
// The tail is scrubbed with the turn's Scrub (carried on ctx) before it is cut.
func (p *proc) exitErr(ctx context.Context, name string) error {
	msg := fmt.Sprintf("%s: the process stopped", name)
	select {
	case <-p.done:
		if p.err != nil {
			msg += " (" + p.err.Error() + ")"
		}
	case <-time.After(2 * time.Second):
	}
	if t := p.tail.text(scrubOf(ctx)); t != "" {
		msg += "; stderr: " + t
	}
	return errors.New(msg)
}

// tailShow is how much of a dead process's stderr an error shows; tailKeep is
// how much is kept to cut it from. Scrubbing a text and then cutting it is the
// only order that holds: a cut that comes first can split a sensitive value,
// and the piece left is no longer recognisable as it. The buffer itself has to
// drop old bytes as they come, though, so a value can be split at its start;
// what is shown is therefore always at least tailKeep-tailShow (60 KB) after
// that point, and a value of which some form is longer than that is the one
// case not covered.
const (
	tailShow = 4096
	tailKeep = 64 << 10
)

// tailBuffer keeps the last tailKeep bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	b   []byte
	cut bool // bytes were dropped from the start
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > tailKeep {
		t.b = append([]byte(nil), t.b[len(t.b)-tailKeep:]...)
		t.cut = true
	}
	return len(p), nil
}

// text is the tail as an error shows it: scrubbed whole, then cut.
func (t *tailBuffer) text(scrub func(string) string) string {
	t.mu.Lock()
	s, cut := string(t.b), t.cut
	t.mu.Unlock()
	return showTail(s, cut, scrub)
}

// showTail scrubs s whole and keeps at most its last tailShow bytes, on a rune
// boundary. When s already lost its start (cut), at least its first
// tailKeep-tailShow bytes go too, whatever the scrub left: that is where a
// piece of a value split by the buffer sits, and a scrub only replaces whole
// values, so the piece stays where it was.
func showTail(s string, cut bool, scrub func(string) string) string {
	if scrub != nil {
		s = scrub(s)
	}
	drop := len(s) - tailShow
	if cut {
		drop = max(drop, tailKeep-tailShow)
	}
	if drop <= 0 {
		return strings.TrimSpace(s)
	}
	if drop >= len(s) {
		return ""
	}
	for drop < len(s) && !utf8.RuneStart(s[drop]) {
		drop++
	}
	if rest := strings.TrimSpace(s[drop:]); rest != "" {
		return "… " + rest
	}
	return ""
}

// scrubKey carries a turn's Scrub on its context, down to where a process's
// death is turned into an error.
type scrubKey struct{}

func withScrub(ctx context.Context, scrub func(string) string) context.Context {
	if scrub == nil {
		return ctx
	}
	return context.WithValue(ctx, scrubKey{}, scrub)
}

func scrubOf(ctx context.Context) func(string) string {
	f, _ := ctx.Value(scrubKey{}).(func(string) string)
	return f
}

// backoff spaces restarts of a process that keeps dying: 250 ms, doubling, at
// most 10 s; one good turn resets it.
type backoff struct {
	fails int
	until time.Time
}

func (b *backoff) wait(ctx context.Context) error {
	d := time.Until(b.until)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *backoff) failed() {
	b.fails++
	d := 250 * time.Millisecond << min(b.fails-1, 6)
	b.until = time.Now().Add(min(d, 10*time.Second))
}

func (b *backoff) ok() { *b = backoff{} }

// Pool shares warm agents among callers. A turn takes an idle agent and gives
// it back; with every agent busy it waits. One agent is one process, so the
// size is how many turns can run at once.
type Pool struct {
	label  string
	agents []WarmAgent
	idle   chan WarmAgent
}

// NewPool makes n agents with mk (n < 1 ⇒ 1).
func NewPool(n int, mk func() WarmAgent) *Pool {
	n = max(n, 1)
	p := &Pool{idle: make(chan WarmAgent, n)}
	for range n {
		a := mk()
		p.agents = append(p.agents, a)
		p.idle <- a
	}
	p.label = p.agents[0].Name()
	return p
}

// Name implements Agent.
func (p *Pool) Name() string { return p.label }

// Execute implements Agent.
func (p *Pool) Execute(ctx context.Context, t Turn) (string, error) {
	var a WarmAgent
	select {
	case a = <-p.idle:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { p.idle <- a }()
	return a.Execute(ctx, t)
}

// Warm warms every agent at once.
func (p *Pool) Warm(ctx context.Context) error {
	errs := make([]error, len(p.agents))
	var wg sync.WaitGroup
	for i, a := range p.agents {
		wg.Go(func() { errs[i] = a.Warm(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Healthy reports whether at least one agent is running.
func (p *Pool) Healthy() bool {
	for _, a := range p.agents {
		if a.Healthy() {
			return true
		}
	}
	return false
}

// Close stops every agent.
func (p *Pool) Close() error {
	var errs []error
	for _, a := range p.agents {
		errs = append(errs, a.Close())
	}
	return errors.Join(errs...)
}
