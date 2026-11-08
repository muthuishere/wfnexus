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
	// Kind is how the transport should present it: file (default), image,
	// voice or sticker.
	Kind string `json:"kind,omitempty"`
}

// SendReaction reacts to a message instead of posting; an empty Emoji removes
// our reaction. Sender is the reacted message's sender (groups need it).
type SendReaction struct {
	TargetID string `json:"targetId"`
	Emoji    string `json:"emoji"`
	Sender   string `json:"sender,omitempty"`
}

// MessageRef names one of OUR sent messages, for an edit or a delete.
type MessageRef struct {
	ID string `json:"id"`
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

	// Richer sends (ADR 0026). They ride the same envelope so EVERY op goes
	// through Send: one at a time per channel, at least Gap apart, and past
	// the adapter's echo ledger. An adapter that cannot do one must fail.
	ReplyTo  *ReplyRef     `json:"replyTo,omitempty"`
	Mentions []string      `json:"mentions,omitempty"`
	Reaction *SendReaction `json:"reaction,omitempty"`
	Edit     *MessageRef   `json:"edit,omitempty"`
	Delete   *MessageRef   `json:"delete,omitempty"`
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

	// ADR 0026 event fields. All optional: an adapter that predates them
	// still produces a valid message.
	Chat      string       `json:"chat,omitempty"`
	ChatName  string       `json:"chatName,omitempty"`
	ChatType  string       `json:"chatType,omitempty"` // "dm" | "group"
	Kind      string       `json:"kind,omitempty"`     // "message" | "reaction" | "updated" | "deleted"
	ReplyTo   *ReplyRef    `json:"replyTo,omitempty"`
	Media     *MediaRef    `json:"media,omitempty"`
	Mentions  []string     `json:"mentions,omitempty"`
	FromOwner bool         `json:"fromOwner,omitempty"`
	Reaction  *ReactionRef `json:"reaction,omitempty"`
}

// ReplyRef is the message a received message quotes.
type ReplyRef struct {
	ID     string `json:"id,omitempty"`
	Sender string `json:"sender,omitempty"`
	Text   string `json:"text,omitempty"`
}

// MediaRef describes an attachment that is fetched on demand.
type MediaRef struct {
	Kind string `json:"kind,omitempty"`
	Mime string `json:"mime,omitempty"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// ReactionRef is a reaction event's target and emoji.
type ReactionRef struct {
	TargetID string `json:"targetId,omitempty"`
	Emoji    string `json:"emoji,omitempty"`
}

// EventKind is Kind with its default, "message".
func (m Message) EventKind() string {
	if m.Kind == "" {
		return "message"
	}
	return m.Kind
}

// ReceiveResult is what `<run> receive` prints. Cursor is persisted and handed
// back as After next time; an empty cursor keeps the previous one.
type ReceiveResult struct {
	Messages []Message `json:"messages"`
	Cursor   string    `json:"cursor"`
	// Notice is something the operator should see even though the receive
	// succeeded — e.g. the cursor was lost and reset. Shown by `wfx channels`
	// and on the System page.
	Notice string `json:"notice,omitempty"`
}

// Command is the one channel adapter: it runs a program.
type Command struct {
	EntryName string
	Run       string
	Env       map[string]string
	Dir       string
	// StateDir is a stable per-server directory the adapter may keep state in
	// (its echo ledger), passed as WFX_CHANNEL_STATE_DIR to every verb so
	// send and receive see the same files. Empty = not set.
	StateDir string
	Timeout  time.Duration
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

// ErrMaybeDelivered marks a send that was cut off rather than refused: the
// adapter may already have delivered it, so it is neither retried nor
// released from dedup.
var ErrMaybeDelivered = errors.New("the message may have been delivered")

// passEnv is the part of the server's environment an adapter inherits: enough
// to find programs, a home and a temp dir — and nothing else. The server's
// environment holds its database URL and provider keys; an adapter is someone
// else's script and gets none of it. What it needs, the channel's `env:` says.
var passEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TZ",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
	// windows
	"SystemRoot", "SYSTEMROOT", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ComSpec", "PATHEXT",
}

// AdapterEnv is the minimal environment plus the channel's own `env:`, whose
// values may reference the server's variables (${NAME}) — the only way one
// reaches an adapter, and only because an operator named it.
func AdapterEnv(extra map[string]string) []string {
	var out []string
	for _, k := range passEnv {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	for k, v := range extra {
		out = append(out, k+"="+os.ExpandEnv(v))
	}
	return out
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
	cmd.Env = AdapterEnv(c.Env)
	// The adapter's own deadline, so it can give up — and say so — before
	// it is killed rather than after.
	cmd.Env = append(cmd.Env, "WFX_CHANNEL="+c.EntryName, "WFX_CHANNEL_VERB="+verb,
		fmt.Sprintf("WFX_CHANNEL_TIMEOUT_MS=%d", timeout.Milliseconds()))
	if _, own := c.Env["WFX_CHANNEL_STATE_DIR"]; c.StateDir != "" && !own {
		if err := os.MkdirAll(c.StateDir, 0o700); err == nil {
			cmd.Env = append(cmd.Env, "WFX_CHANNEL_STATE_DIR="+c.StateDir)
		}
	}
	cmd.Stdin = bytes.NewReader(body)
	// A grandchild that outlives the killed shell holds stdout open; without
	// this a timed-out adapter hangs the caller until that child exits.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > MaxStderr {
			msg = msg[len(msg)-MaxStderr:]
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, fmt.Errorf("%w: channel %s: %s timed out after %s (not retried): %s", ErrMaybeDelivered, c.EntryName, verb, timeout, msg)
		}
		// The adapter may say on stdout whether a failed send could still
		// have gone out: {"status":"uncertain"} keeps the dedup key.
		var said struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		_ = json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &said)
		if msg == "" {
			msg = said.Error
		}
		if msg == "" {
			msg = err.Error()
		}
		if said.Status == "uncertain" {
			return nil, fmt.Errorf("%w: channel %s: %s could not confirm (not retried): %s", ErrMaybeDelivered, c.EntryName, verb, msg)
		}
		return nil, fmt.Errorf("channel %s: %s failed: %s", c.EntryName, verb, msg)
	}
	return stdout.Bytes(), nil
}
