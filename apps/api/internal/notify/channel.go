package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/shell"
)

// A CHANNEL is the two-way sibling of a notifier (ADR 0023): the server sends
// messages through it and, when it is inbound, reads replies from it.
//
// wfnexus does not speak any transport. A channel is a COMMAND — an adapter
// script someone else owns — and the contract is two verbs over stdin/stdout:
//
//	<run> send     stdin: Envelope              stdout: SendResult (optional)
//	<run> receive  stdin: ReceiveRequest        stdout: ReceiveResult
//
// Exit 0 is success; anything else is a failure whose stderr is shown on the
// run. docs/channels.md is the human contract and docs/schemas/ the machine one.
//
// SECURITY, the same rule as a notifier: an envelope carries a POINTER (the run
// URL) and a short pause code, never a capability. The code alone resolves
// nothing — only a reply carrying it FROM a sender on the channel's allow list.

// Version is the contract version every envelope and request carries.
const Version = "wfnexus.channel/v1"

// Envelope types.
const (
	TypePause  = "pause"  // a run stopped and needs a person
	TypeNotice = "notice" // the platform telling someone something
	TypeSend   = "send"   // a workflow step or an operator sending a message
)

// Attachment is a file to send: a local Path the adapter can read, or a URL
// it can fetch. Exactly one is set.
type Attachment struct {
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// Envelope is what `<run> send` reads on stdin.
type Envelope struct {
	Version string `json:"version"`
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Text    string `json:"text"`
	// RunURL is where a person goes to look — a pointer at our UI, never a
	// resolve-on-click link.
	RunURL string `json:"runUrl,omitempty"`
	// Code is the short pause code (`a7k2`, written `#a7k2` in the text) a
	// reply quotes to answer this pause. Pause envelopes only.
	Code      string `json:"code,omitempty"`
	PauseKind string `json:"pauseKind,omitempty"`
	Workflow  string `json:"workflow,omitempty"`
	Project   string `json:"project,omitempty"`
	RunID     string `json:"runId,omitempty"`
	StepID    string `json:"stepId,omitempty"`
	DedupKey  string `json:"dedupKey,omitempty"`
	// Target is an adapter-specific address (a group, a mailbox); opaque here.
	Target     string      `json:"target,omitempty"`
	Attachment *Attachment `json:"attachment,omitempty"`
	At         time.Time   `json:"at"`
}

// SendResult is what `<run> send` MAY print. An empty stdout is fine.
type SendResult struct {
	ID string `json:"id,omitempty"`
}

// ReceiveRequest is what `<run> receive` reads on stdin. After is the cursor
// the adapter returned last time; "" on the first poll.
type ReceiveRequest struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	After   string `json:"after"`
	Target  string `json:"target,omitempty"`
}

// InboundAttachment is a received file, by reference.
type InboundAttachment struct {
	URL      string `json:"url,omitempty"`
	Filename string `json:"filename,omitempty"`
	Mime     string `json:"mime,omitempty"`
}

// Message is one received message. ID is the adapter's own id and is what
// makes a re-delivered message a no-op; From is the sender id the allow list
// is matched against.
type Message struct {
	ID          string              `json:"id"`
	From        string              `json:"from"`
	Name        string              `json:"name,omitempty"`
	Text        string              `json:"text"`
	At          *time.Time          `json:"at,omitempty"`
	Attachments []InboundAttachment `json:"attachments,omitempty"`
}

// ReceiveResult is what `<run> receive` prints. Cursor is persisted and handed
// back as After next time; an empty cursor keeps the previous one.
type ReceiveResult struct {
	Messages []Message `json:"messages"`
	Cursor   string    `json:"cursor"`
}

// Command is the one channel adapter: it runs a program.
type Command struct {
	EntryName string
	Run       string
	Env       map[string]string
	Dir       string
	Timeout   time.Duration
	// Gap is the minimum spacing between two sends on this channel.
	Gap time.Duration
}

func (c Command) Name() string { return c.EntryName }

// Sends on one channel go out ONE AT A TIME, process-wide, and at least Gap
// apart: a chat transport rate-limits (or bans) a burst, and two concurrent
// runs must not become one.
var (
	sendMu   sync.Mutex
	sendLock = map[string]*sync.Mutex{}
	lastSend = map[string]time.Time{}
)

func channelLock(name string) *sync.Mutex {
	sendMu.Lock()
	defer sendMu.Unlock()
	m := sendLock[name]
	if m == nil {
		m = &sync.Mutex{}
		sendLock[name] = m
	}
	return m
}

// Send runs `<run> send` with the envelope on stdin. It is never retried
// here: an adapter that timed out may still have delivered.
func (c Command) Send(ctx context.Context, env Envelope) (SendResult, error) {
	if env.Version == "" {
		env.Version = Version
	}
	if env.Channel == "" {
		env.Channel = c.EntryName
	}
	if env.At.IsZero() {
		env.At = time.Now().UTC()
	}
	lock := channelLock(c.EntryName)
	lock.Lock()
	defer lock.Unlock()
	if c.Gap > 0 {
		sendMu.Lock()
		wait := c.Gap - time.Since(lastSend[c.EntryName])
		sendMu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return SendResult{}, ctx.Err()
			}
		}
	}
	out, err := c.exec(ctx, "send", env)
	sendMu.Lock()
	lastSend[c.EntryName] = time.Now()
	sendMu.Unlock()
	if err != nil {
		return SendResult{}, err
	}
	var res SendResult
	if s := bytes.TrimSpace(out); len(s) > 0 {
		// Stdout is optional; something that is not our JSON is ignored
		// rather than failing a send that did happen.
		_ = json.Unmarshal(s, &res)
	}
	return res, nil
}

// Receive runs `<run> receive` with the cursor on stdin.
func (c Command) Receive(ctx context.Context, req ReceiveRequest) (ReceiveResult, error) {
	if req.Version == "" {
		req.Version = Version
	}
	if req.Channel == "" {
		req.Channel = c.EntryName
	}
	out, err := c.exec(ctx, "receive", req)
	if err != nil {
		return ReceiveResult{}, err
	}
	var res ReceiveResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil {
		return ReceiveResult{}, fmt.Errorf("channel %s: receive printed something that is not %s JSON: %w", c.EntryName, Version, err)
	}
	return res, nil
}

// MaxStderr bounds how much of an adapter's stderr reaches an error (and so a
// run event).
const MaxStderr = 2000

func (c Command) exec(ctx context.Context, verb string, in any) ([]byte, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sh, err := shell.Default()
	if err != nil {
		return nil, err
	}
	argv := sh.Command(c.Run + " " + verb)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = os.Environ()
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+os.ExpandEnv(v))
	}
	cmd.Env = append(cmd.Env, "WFX_CHANNEL="+c.EntryName, "WFX_CHANNEL_VERB="+verb)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > MaxStderr {
			msg = msg[len(msg)-MaxStderr:]
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("channel %s: %s timed out after %s (it may still have happened; not retried): %s", c.EntryName, verb, timeout, msg)
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("channel %s: %s failed: %s", c.EntryName, verb, msg)
	}
	return stdout.Bytes(), nil
}
