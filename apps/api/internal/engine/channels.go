package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Channels (ADR 0023): the server's one way of sending and receiving messages.
// A channel is a registry entry naming an adapter COMMAND; this file runs it,
// records what came back, and turns replies into pause answers and runs.

// channelStore is the persistence channels need. It is a separate interface
// from Store so a worker engine and the test fakes that predate channels are
// untouched; an engine whose store lacks it simply has no channels.
type channelStore interface {
	InsertChannelMessage(ctx context.Context, m *model.ChannelMessage) (bool, error)
	SetChannelMessageOutcome(ctx context.Context, channel, msgID, runID, outcome string) error
	ListChannelMessages(ctx context.Context, channel, runID string, limit int) ([]*model.ChannelMessage, error)
	PendingChannelMessages(ctx context.Context, channel string) ([]*model.ChannelMessage, error)
	DedupClaim(ctx context.Context, channel, key string, window time.Duration) (bool, time.Time, error)
	DedupRelease(ctx context.Context, channel, key string) error
	InsertPauseCode(ctx context.Context, c model.PauseCode) (bool, error)
	RetirePauseCodes(ctx context.Context, runID, stepID string) error
	PauseByCode(ctx context.Context, channel, code string) (*model.PauseCode, error)
	PruneChannels(ctx context.Context, msgsBefore, codesBefore time.Time) error
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

func (e *Engine) chStore() (channelStore, bool) {
	cs, ok := e.store.(channelStore)
	return cs, ok
}

// ChannelInfo is a channel as the API and CLI show it.
type ChannelInfo struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Kind         string   `json:"kind"`
	Default      bool     `json:"default"`
	Inbound      bool     `json:"inbound"`
	ReceiveEvery string   `json:"receiveEvery,omitempty"`
	Allow        []string `json:"allow"`
	Cursor       string   `json:"cursor,omitempty"`
	LastReceive  string   `json:"lastReceive,omitempty"`
	LastError    string   `json:"lastError,omitempty"`
	// LastNotice is what the adapter said needs attention even though the
	// receive worked — a lost cursor that was reset, say.
	LastNotice string `json:"lastNotice,omitempty"`
}

// channelHealth is the last receive outcome per channel, for `wfx channels`
// and the System page.
type channelHealth struct {
	mu      sync.Mutex
	at      map[string]time.Time
	errs    map[string]string
	notices map[string]string
}

var chHealth = channelHealth{at: map[string]time.Time{}, errs: map[string]string{}, notices: map[string]string{}}

// Retention for what channels keep (pruned by the receive loop).
const (
	pauseCodeTTL       = 7 * 24 * time.Hour
	keepChannelMsgs    = 90 * 24 * time.Hour
	keepRetiredCodes   = 30 * 24 * time.Hour
	channelPruneEvery  = time.Hour
	receiveTickEvery   = 10 * time.Second
	defaultReceiveEach = time.Minute
)

// DefaultChannel is the configured server-wide channel, "" when none.
func (e *Engine) DefaultChannel() string { return e.cfg.DefaultChannel }

func (e *Engine) channelEntry(name string) (catalog.Channel, error) {
	e.mu.Lock()
	cat := e.catalog
	e.mu.Unlock()
	if name == "" {
		name = e.cfg.DefaultChannel
	}
	if name == "" {
		return catalog.Channel{}, fmt.Errorf("no channel named and no default channel configured (channels.default / WFX_DEFAULT_CHANNEL)")
	}
	if cat == nil || cat.Channels == nil {
		return catalog.Channel{}, fmt.Errorf("channel %q is not registered (registries.json `channels`)", name)
	}
	c, ok := cat.Channels.Get(name)
	if !ok {
		return catalog.Channel{}, fmt.Errorf("channel %q is not registered (registries.json `channels`)", name)
	}
	return c, nil
}

func (e *Engine) adapter(c catalog.Channel) notify.Command {
	cmd := notify.Command{EntryName: c.Name, Run: c.Run, Env: c.Env}
	// Relative to registries.json, so `node adapters/x.js` means the file
	// beside the registry that names it.
	if e.cfg.RegistriesPath != "" {
		cmd.Dir = filepath.Dir(e.cfg.RegistriesPath)
	}
	if c.TimeoutSec > 0 {
		cmd.Timeout = time.Duration(c.TimeoutSec) * time.Second
	}
	if d, err := time.ParseDuration(c.SendGap); err == nil {
		cmd.Gap = d
	}
	return cmd
}

// adapterErr scrubs an adapter's error before it reaches an event, a step
// error or a log — stderr is someone else's output and may echo a credential —
// while keeping whether it may have been delivered.
func adapterErr(err error) error {
	if err == nil {
		return nil
	}
	msg := scrub(err.Error())
	if errors.Is(err, notify.ErrMaybeDelivered) {
		return fmt.Errorf("%w: %s", notify.ErrMaybeDelivered, strings.TrimPrefix(msg, notify.ErrMaybeDelivered.Error()+": "))
	}
	return errors.New(msg)
}

// Channels lists every registered channel.
func (e *Engine) Channels(ctx context.Context) []ChannelInfo {
	e.mu.Lock()
	cat := e.catalog
	e.mu.Unlock()
	out := []ChannelInfo{}
	if cat == nil || cat.Channels == nil {
		return out
	}
	cs, _ := e.chStore()
	for _, c := range cat.Channels.List() {
		info := ChannelInfo{Name: c.Name, Description: c.Description, Kind: c.Kind,
			Default: c.Name == e.cfg.DefaultChannel, Inbound: c.Inbound, ReceiveEvery: c.ReceiveEvery, Allow: c.Allow}
		if info.Allow == nil {
			info.Allow = []string{}
		}
		if cs != nil {
			info.Cursor, _ = cs.Setting(ctx, channelCursorKey(c.Name))
		}
		chHealth.mu.Lock()
		if t, ok := chHealth.at[c.Name]; ok {
			info.LastReceive = t.UTC().Format(time.RFC3339)
		}
		info.LastError = chHealth.errs[c.Name]
		info.LastNotice = chHealth.notices[c.Name]
		chHealth.mu.Unlock()
		out = append(out, info)
	}
	return out
}

// channelCursorKey matches store.ChannelCursorKey; spelled here so the engine
// does not import the store.
func channelCursorKey(name string) string { return "channel.cursor." + name }

// SendOnChannel sends one envelope. name "" is the default channel. It
// returns the channel actually used. A URL attachment is checked here, on
// every path into an adapter, so no caller can hand one an internal address.
func (e *Engine) SendOnChannel(ctx context.Context, name string, env notify.Envelope) (string, notify.SendResult, error) {
	c, err := e.channelEntry(name)
	if err != nil {
		return "", notify.SendResult{}, err
	}
	if env.Attachment != nil && env.Attachment.URL != "" {
		if err := notify.CheckAttachmentURL(ctx, env.Attachment.URL); err != nil {
			return c.Name, notify.SendResult{}, err
		}
	}
	env.Channel = c.Name
	if env.Target == "" {
		env.Target = c.Target
	}
	if env.Type == "" {
		env.Type = notify.TypeSend
	}
	res, err := e.adapter(c).Send(ctx, env)
	return c.Name, res, adapterErr(err)
}

// ChannelMessages lists received messages, newest first.
func (e *Engine) ChannelMessages(ctx context.Context, channel, runID string, limit int) ([]*model.ChannelMessage, error) {
	cs, ok := e.chStore()
	if !ok {
		return []*model.ChannelMessage{}, nil
	}
	return cs.ListChannelMessages(ctx, channel, runID, limit)
}

// ---- pause codes ----

// codeAlphabet leaves out the characters a person misreads (0/o, 1/l/i).
const codeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// codeLen is 6: 31^6 ≈ 887M. The code is a lookup, not a credential — an
// answer also needs an allow-listed sender — but it should not be guessable
// from a handful of tries either.
const codeLen = 6

func newPauseCode() string {
	b := make([]byte, codeLen)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}

// issuePauseCode retires any earlier code for this step (a step that paused
// again must not be answerable by the old one) and records a fresh code on
// the channel it will be sent on. A collision draws another code; an existing
// row is never re-pointed.
func (e *Engine) issuePauseCode(ctx context.Context, channel string, runID uuid.UUID, stepID, kind string, expires time.Time) (string, error) {
	cs, ok := e.chStore()
	if !ok {
		return "", fmt.Errorf("this store keeps no pause codes")
	}
	if err := cs.RetirePauseCodes(ctx, runID.String(), stepID); err != nil {
		return "", err
	}
	if expires.IsZero() || expires.After(time.Now().Add(pauseCodeTTL)) {
		expires = time.Now().Add(pauseCodeTTL)
	}
	for i := 0; i < 8; i++ {
		code := newPauseCode()
		ok, err := cs.InsertPauseCode(ctx, model.PauseCode{Channel: channel, Code: code, RunID: runID.String(),
			StepID: stepID, Kind: kind, ExpiresAt: expires})
		if err != nil {
			return "", err
		}
		if ok {
			return code, nil
		}
	}
	return "", fmt.Errorf("could not find a free pause code")
}

// retirePause retires a step's codes once it is resolved by ANY path — the UI,
// the CLI or a chat reply — so a late or replayed reply matches nothing.
func (e *Engine) retirePause(ctx context.Context, runID uuid.UUID, stepID string) {
	if cs, ok := e.chStore(); ok {
		if err := cs.RetirePauseCodes(context.WithoutCancel(ctx), runID.String(), stepID); err != nil {
			log.Printf("engine: retire pause codes: %v", err)
		}
	}
}

// pauseText is the human message for a pause. It says how to answer and where
// to look; it carries nothing that answers by itself.
func pauseText(p notify.Pause, code string) string {
	var b strings.Builder
	name := p.Workflow
	if name == "" {
		name = "a run"
	}
	fmt.Fprintf(&b, "%s is waiting (%s) at step %s", name, p.Kind, p.StepID)
	if p.Prompt != "" {
		fmt.Fprintf(&b, ":\n%s", p.Prompt)
	}
	if code != "" {
		if p.Kind == "approval" {
			fmt.Fprintf(&b, "\n\nReply \"#%s yes\" or \"#%s no <reason>\".", code, code)
		} else {
			fmt.Fprintf(&b, "\n\nReply \"#%s <your answer>\".", code)
		}
	}
	if p.URL != "" {
		fmt.Fprintf(&b, "\nOpen: %s", p.URL)
	}
	return b.String()
}

// announcePause sends a pause to the default channel, with a code. It runs
// in the BACKGROUND: the run has already parked, and an adapter that takes a
// minute must not hold the run's goroutine (or its slot) for that minute.
func (e *Engine) announcePause(ctx context.Context, runID uuid.UUID, p notify.Pause) {
	c, err := e.channelEntry("")
	if err != nil {
		if e.cfg.DefaultChannel != "" {
			e.emit(ctx, runID, p.StepID, "log", map[string]any{"text": "channel: " + err.Error() + " (the run is still paused)"})
		}
		return
	}
	bg := context.WithoutCancel(ctx)
	timeout := 60 * time.Second
	if c.TimeoutSec > 0 {
		timeout = time.Duration(c.TimeoutSec) * time.Second
	}
	if d, err := time.ParseDuration(c.SendGap); err == nil {
		timeout += d // a send may first wait out the channel's gap
	}
	go func() {
		ctx, cancel := context.WithTimeout(bg, timeout+10*time.Second)
		defer cancel()
		var expires time.Time
		if p.ExpiresAt != nil {
			expires = *p.ExpiresAt
		}
		code, err := e.issuePauseCode(ctx, c.Name, runID, p.StepID, p.Kind, expires)
		if err != nil {
			log.Printf("engine: pause code: %v", err)
			code = ""
		}
		env := notify.Envelope{
			Type: notify.TypePause, Text: pauseText(p, code), RunURL: p.URL, Code: code, PauseKind: p.Kind,
			Workflow: p.Workflow, Project: p.Project, RunID: p.RunID, StepID: p.StepID,
			DedupKey: "pause:" + p.RunID + ":" + p.StepID, At: p.At,
		}
		if _, _, err := e.SendOnChannel(ctx, c.Name, env); err != nil {
			e.emit(ctx, runID, p.StepID, "log", map[string]any{
				"text": "channel " + c.Name + " failed: " + err.Error() + " (the run is still paused)",
			})
			return
		}
		e.emit(ctx, runID, p.StepID, "log", map[string]any{
			"text": "pause (" + p.Kind + ") announced on channel " + c.Name + " with code #" + code + " → " + p.URL,
		})
	}()
}

// ---- the send step ----

// runSend executes a `send:` step.
func (e *Engine) runSend(ctx context.Context, runID uuid.UUID, step *workflow.Step, data workflow.TemplateData) (map[string]any, error) {
	s := step.Send
	e.setStep(ctx, runID, step.ID, model.StepPatch{Status: str("running"), StartedAt: now(), Error: str(""), ClearPending: true})
	render := func(field, t string) (string, error) {
		if t == "" {
			return "", nil
		}
		v, err := workflow.Render(t, data)
		if err != nil {
			return "", fmt.Errorf("render send.%s: %w", field, err)
		}
		return v, nil
	}
	text, err := render("text", s.Text)
	if err != nil {
		return nil, err
	}
	attach, err := render("attachment", s.Attachment)
	if err != nil {
		return nil, err
	}
	target, err := render("target", s.Target)
	if err != nil {
		return nil, err
	}
	c, err := e.channelEntry(s.Channel)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"sent": false, "channel": c.Name, "text": text}
	env := notify.Envelope{Type: notify.TypeSend, Text: text, Target: target, RunURL: e.pauseURL(runID),
		RunID: runID.String(), StepID: step.ID}
	if run, err := e.store.GetRun(ctx, runID); err == nil && run != nil {
		env.Workflow, env.Project = run.Workflow, run.Project
	}
	if attach != "" {
		a, err := sendAttachment(attach, data.WorkDir)
		if err != nil {
			return nil, err
		}
		env.Attachment = a
	}
	e.emit(ctx, runID, step.ID, "tool_call", map[string]any{"name": "send", "args": map[string]any{"channel": c.Name, "text": text}})

	if s.DryRun {
		e.emit(ctx, runID, step.ID, "log", map[string]any{"text": "dry run: would send on channel " + c.Name + ": " + text})
		out["skipped"] = "dry_run"
		return out, nil
	}
	var key string
	if s.Dedup != nil {
		if key, err = render("dedup.key", s.Dedup.Key); err != nil {
			return nil, err
		}
		env.DedupKey = key
		window, _ := s.Dedup.WindowDuration()
		cs, ok := e.chStore()
		if !ok {
			return nil, fmt.Errorf("dedup needs the server's store")
		}
		claimed, last, err := cs.DedupClaim(ctx, c.Name, key, window)
		if err != nil {
			return nil, err
		}
		if !claimed {
			e.emit(ctx, runID, step.ID, "log", map[string]any{
				"text": fmt.Sprintf("skipped: %q was already sent on %s at %s (dedup window %s)", key, c.Name, last.UTC().Format(time.RFC3339), s.Dedup.Window),
			})
			out["skipped"] = "dedup"
			return out, nil
		}
	}
	_, res, err := e.SendOnChannel(ctx, c.Name, env)
	if err != nil {
		// A send that was REFUSED releases its key so the next run can try
		// again. One that was cut off (a timeout) keeps it: it may well have
		// been delivered, and a second copy is the thing dedup exists to stop.
		if key != "" && !errors.Is(err, notify.ErrMaybeDelivered) {
			if cs, ok := e.chStore(); ok {
				_ = cs.DedupRelease(context.WithoutCancel(ctx), c.Name, key)
			}
		}
		e.emit(ctx, runID, step.ID, "tool_result", map[string]any{"name": "send", "isError": true, "output": err.Error()})
		return nil, err
	}
	out["sent"] = true
	if res.ID != "" {
		out["id"] = res.ID
	}
	e.emit(ctx, runID, step.ID, "tool_result", map[string]any{"name": "send", "isError": false, "output": "sent on " + c.Name})
	return out, nil
}

// sendAttachment turns a send step's rendered attachment into the envelope's.
// A path must stay inside the run's workspace: a template must not be able to
// post /etc/passwd (or the server's config) to a chat.
func sendAttachment(attach, workDir string) (*notify.Attachment, error) {
	if strings.Contains(attach, "://") {
		// Checked again, with DNS, by SendOnChannel.
		return &notify.Attachment{URL: attach}, nil
	}
	if workDir == "" {
		return nil, fmt.Errorf("send.attachment %q: a file attachment needs the run's workspace", attach)
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	p := attach
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = realRoot
	}
	if rel, err := filepath.Rel(root, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("send.attachment %q is outside the run's workspace; refused", attach)
	}
	return &notify.Attachment{Path: p, Filename: filepath.Base(p)}, nil
}

// ---- receiving ----

// StartChannelReceivers polls every inbound channel on its own interval, each
// in its own goroutine so one slow adapter does not delay the others. Like the
// scheduler it is per-process (docs/not-now.md). It also prunes old messages
// and pause codes.
func (e *Engine) StartChannelReceivers(ctx context.Context) {
	go func() {
		t := time.NewTicker(receiveTickEvery)
		defer t.Stop()
		var mu sync.Mutex
		next := map[string]time.Time{}
		busy := map[string]bool{}
		var lastPrune time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if now.Sub(lastPrune) > channelPruneEvery {
					lastPrune = now
					if cs, ok := e.chStore(); ok {
						if err := cs.PruneChannels(ctx, now.Add(-keepChannelMsgs), now.Add(-keepRetiredCodes)); err != nil {
							log.Printf("channels: prune: %v", err)
						}
					}
				}
				e.mu.Lock()
				cat := e.catalog
				e.mu.Unlock()
				if cat == nil || cat.Channels == nil {
					continue
				}
				for _, c := range cat.Channels.List() {
					mu.Lock()
					due := c.Inbound && !busy[c.Name] && !now.Before(next[c.Name])
					if due {
						every := defaultReceiveEach
						if d, err := time.ParseDuration(c.ReceiveEvery); err == nil && d > 0 {
							every = d
						}
						next[c.Name] = now.Add(every)
						busy[c.Name] = true
					}
					mu.Unlock()
					if !due {
						continue
					}
					go func(name string) {
						defer func() { mu.Lock(); busy[name] = false; mu.Unlock() }()
						if _, err := e.ReceiveNow(ctx, name); err != nil {
							log.Printf("channels: %v", err)
						}
					}(c.Name)
				}
			}
		}
	}()
}

// receiveLocks keeps one receive per channel at a time, whoever asked — the
// poller or `wfx channels receive`.
var receiveLocks sync.Map

// ReceiveNow runs one receive on a channel and handles what came back. It
// returns how many NEW messages were stored.
//
// Order is what makes it crash-safe: messages left pending by a previous poll
// are handled first; new ones are STORED (pending) before any is acted on; the
// cursor moves only after they are all stored; each is marked handled after
// it is acted on. A crash anywhere re-handles rather than loses.
func (e *Engine) ReceiveNow(ctx context.Context, name string) (int, error) {
	c, err := e.channelEntry(name)
	if err != nil {
		return 0, err
	}
	if !c.Inbound {
		return 0, fmt.Errorf("channel %s is not inbound", c.Name)
	}
	cs, ok := e.chStore()
	if !ok {
		return 0, fmt.Errorf("this store cannot keep received messages")
	}
	lk, _ := receiveLocks.LoadOrStore(c.Name, &sync.Mutex{})
	lk.(*sync.Mutex).Lock()
	defer lk.(*sync.Mutex).Unlock()

	if err := e.handlePending(ctx, c, cs); err != nil {
		return 0, err
	}
	cursor, err := cs.Setting(ctx, channelCursorKey(c.Name))
	if err != nil {
		return 0, err
	}
	res, err := e.adapter(c).Receive(ctx, notify.ReceiveRequest{After: cursor, Target: c.Target})
	err = adapterErr(err)
	chHealth.mu.Lock()
	chHealth.at[c.Name] = time.Now()
	if err != nil {
		chHealth.errs[c.Name] = err.Error()
	} else {
		delete(chHealth.errs, c.Name)
		if res.Notice != "" {
			chHealth.notices[c.Name] = time.Now().UTC().Format(time.RFC3339) + " " + scrub(res.Notice)
		}
	}
	chHealth.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if res.Notice != "" {
		log.Printf("channels: %s: %s", c.Name, scrub(res.Notice))
	}
	n := 0
	for _, m := range res.Messages {
		if m.ID == "" {
			continue
		}
		att, _ := json.Marshal(m.Attachments)
		if m.Attachments == nil {
			att = []byte("[]")
		}
		row := &model.ChannelMessage{Channel: c.Name, MsgID: m.ID, Sender: m.From, SenderName: m.Name,
			Text: m.Text, Attachments: att, SentAt: m.At}
		fresh, err := cs.InsertChannelMessage(ctx, row)
		if err != nil {
			return n, err
		}
		if fresh {
			n++
		}
	}
	if res.Cursor != "" && res.Cursor != cursor {
		if err := cs.SetSetting(ctx, channelCursorKey(c.Name), res.Cursor); err != nil {
			return n, err
		}
	}
	return n, e.handlePending(ctx, c, cs)
}

// handlePending acts on every stored-but-unhandled message, oldest first.
func (e *Engine) handlePending(ctx context.Context, c catalog.Channel, cs channelStore) error {
	pend, err := cs.PendingChannelMessages(ctx, c.Name)
	if err != nil {
		return err
	}
	for _, row := range pend {
		m := notify.Message{ID: row.MsgID, From: row.Sender, Name: row.SenderName, Text: row.Text, At: row.SentAt}
		if len(row.Attachments) > 0 {
			_ = json.Unmarshal(row.Attachments, &m.Attachments)
		}
		runID, outcome := e.handleInbound(ctx, c, m)
		if err := cs.SetChannelMessageOutcome(ctx, c.Name, m.ID, runID, outcome); err != nil {
			return err
		}
	}
	return nil
}

// codeRe finds pause codes in a reply: `#` and 4–8 of the code alphabet. The
// length is loose on purpose so a code from before the 6-character change is
// still FOUND — and then simply does not match a live row.
var codeRe = regexp.MustCompile(`(?i)#([a-z0-9]{4,8})\b`)

var (
	yesWords = map[string]bool{"yes": true, "y": true, "approve": true, "approved": true, "ok": true, "okay": true, "lgtm": true}
	noWords  = map[string]bool{"no": true, "n": true, "reject": true, "rejected": true, "deny": true, "denied": true}
)

// handleInbound decides what one new message does: answer a pause (when it
// quotes a live code issued ON THIS CHANNEL), or start the workflows whose
// `message:` trigger matches and whose allow list holds the sender.
func (e *Engine) handleInbound(ctx context.Context, c catalog.Channel, m notify.Message) (string, string) {
	cs, _ := e.chStore()
	for _, sm := range codeRe.FindAllStringSubmatch(m.Text, -1) {
		pc, err := cs.PauseByCode(ctx, c.Name, strings.ToLower(sm[1]))
		if err != nil || pc == nil {
			continue
		}
		rest := strings.TrimSpace(strings.Replace(m.Text, sm[0], "", 1))
		if !pc.Live(time.Now()) {
			if runID, err := uuid.Parse(pc.RunID); err == nil {
				e.emit(ctx, runID, pc.StepID, "log", map[string]any{
					"text": "reply on channel " + c.Name + " from " + m.From + " quotes #" + pc.Code + ", which is no longer live — nothing resolved",
				})
			}
			return pc.RunID, "recorded"
		}
		return pc.RunID, e.answerFromChat(ctx, c, m, pc, rest)
	}
	started, refused := 0, 0
	for _, def := range workflow.Sorted(e.Definitions()) {
		matching := def.On.MatchingMessageTriggers(c.Name, m.Text)
		if len(matching) == 0 {
			continue
		}
		allowed := false
		for _, mt := range matching {
			if mt.SenderAllowed(c.Allow, m.From) {
				allowed = true
				break
			}
		}
		if !allowed {
			// The sender matched the text but is on no allow list: anyone in a
			// group must not be able to start a run (or feed it input).
			refused++
			log.Printf("channels: %s: message from %s matches %s's trigger, but the sender is not on its allow list (trigger allow, else channel allow) — not started",
				c.Name, m.From, def.Name)
			continue
		}
		input := map[string]any{"message": map[string]any{
			"channel": c.Name, "id": m.ID, "from": m.From, "name": m.Name, "text": m.Text,
			"at": m.At, "attachments": m.Attachments,
		}}
		if err := e.StartTriggeredBy(ctx, def.Name, workflow.TriggerMessage, input, m.From); err != nil {
			log.Printf("channels: %s: message trigger for %s: %v", c.Name, def.Name, err)
			continue
		}
		started++
	}
	switch {
	case started > 0:
		return "", "triggered"
	case refused > 0:
		return "", "refused"
	}
	return "", ""
}

// answerFromChat resolves a pause from a reply — ONLY when the sender is on
// the channel's allow list. Every other reply is recorded on the run and
// resolves nothing (ADR 0023).
func (e *Engine) answerFromChat(ctx context.Context, c catalog.Channel, m notify.Message, pc *model.PauseCode, rest string) string {
	runID, err := uuid.Parse(pc.RunID)
	if err != nil {
		return "recorded"
	}
	who := m.From
	if m.Name != "" {
		who = m.Name + " <" + m.From + ">"
	}
	if !c.Allows(m.From) {
		e.emit(ctx, runID, pc.StepID, "log", map[string]any{
			"text": "reply on channel " + c.Name + " from " + who + ": " + rest + " — not on the channel's allow list, so it resolves nothing",
		})
		return "recorded"
	}
	word, reason := rest, ""
	if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
		word, reason = rest[:i], strings.TrimSpace(rest[i+1:])
	}
	word = strings.ToLower(strings.Trim(word, ".,!:;"))
	by := Actor{ID: m.From, Via: "channel:" + c.Name, Authenticated: true}
	var ans tn.Answer
	switch {
	case pc.Kind == "approval" && yesWords[word]:
		ans = tn.Answer{Ok: true}
	case pc.Kind == "approval" && noWords[word]:
		if reason == "" {
			reason = "rejected on " + c.Name
		}
		ans = tn.Answer{Ok: false, Reason: "declined", Data: map[string]any{AnswerReason: reason}}
	case pc.Kind == "approval":
		e.emit(ctx, runID, pc.StepID, "log", map[string]any{
			"text": "reply on channel " + c.Name + " from " + who + " is neither yes nor no: " + rest + " — nothing resolved",
		})
		return "recorded"
	case noWords[word] && reason == "":
		ans = tn.Answer{Ok: false, Reason: "declined"}
	case rest == "":
		return "recorded"
	default:
		ans = tn.Answer{Ok: true, Data: map[string]any{tn.RelayOutputKey: rest}}
	}
	if err := e.Resolve(ctx, runID, pc.StepID, ans, by); err != nil {
		e.emit(ctx, runID, pc.StepID, "log", map[string]any{
			"text": "reply on channel " + c.Name + " from " + who + " could not resolve: " + err.Error(),
		})
		return "recorded"
	}
	e.emit(ctx, runID, pc.StepID, "log", map[string]any{
		"text": "answered on channel " + c.Name + " by " + who + ": " + rest,
	})
	return "resolved"
}
