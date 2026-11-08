// Package wamonitor is the whatsapp-group-monitor SERVICE module (ADR 0028,
// issue #84): one long-running process per WhatsApp account that reads the
// account ONCE per tick for every registered watch and delivers matching
// messages to workflow hook URLs (ADR 0027).
//
// It does not talk WhatsApp itself. Each tick runs the existing channel
// adapter's `receive` verb in inbox mode (adapters/apl-whatsapp.js), which
// already owns the hard parts: the "<chat>|<id>" cursor, the read-only store
// poll through apl/wacli, and the echo ledger that drops our own sends. On
// top of that this package adds what a per-run adapter cannot:
//
//   - single instance: an exclusive lock per account (lock_*.go);
//   - one combined read per tick across all watches (the inbox read);
//   - dedupe: a bounded seen-id set that survives restarts;
//   - per-chat debounce: a chat's messages are held until it has been quiet
//     for Debounce (default 3s), then delivered as one batch;
//   - delivery: POST to each matching watch's hook with its bearer token,
//     which is read from an env var the watch NAMES (never stored).
//
// State (cursor + seen ids) is one JSON file, written atomically after each
// successful tick. A failed delivery keeps the batch queued and retries next
// tick; the cursor only covers what was read, never what was delivered.
package wamonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Watch is one registration: a chat (or "*") → a workflow hook.
type Watch struct {
	Name string `json:"name"`
	// Chat is a JID, or "*" for every chat the account sees.
	Chat string `json:"chat"`
	// Kinds filters event kinds (message, reaction, updated, deleted);
	// empty = message only.
	Kinds []string `json:"kinds,omitempty"`
	// Hook is the workflow hook URL: https://host/hooks/{project}/{workflow}.
	Hook string `json:"hook"`
	// TokenEnv names the env var holding the hook's bearer token.
	TokenEnv string `json:"tokenEnv"`
	// How the workflow should handle what this watch delivers. Passed through
	// as input.watch; the workflow decides the defaults (draft: never auto-send).
	ResponsePolicy string   `json:"responsePolicy,omitempty"` // draft | approval | auto | none
	Instructions   string   `json:"instructions,omitempty"`
	Skills         []string `json:"skills,omitempty"`
}

// Event is one received item (docs/channels.md); kept as raw JSON so every
// field the adapter emits reaches the workflow unchanged.
type Event map[string]any

func (e Event) str(k string) string { s, _ := e[k].(string); return s }

// Config wires a Monitor.
type Config struct {
	// Receive is the adapter command; the monitor appends nothing and writes
	// {"after": cursor} on stdin. E.g. ["node", "adapters/apl-whatsapp.js", "receive"].
	Receive []string
	// Env is extra env for the adapter (APL_WA_GROUP=* is always set).
	Env []string
	// Channel names the channel on every delivered message (message.channel),
	// so a workflow keys its conversation on (channel, chat). Default whatsapp.
	Channel string
	// Watches returns the registered watches. It is asked once per read, so
	// a watch registered through wfnexus takes effect without a restart; on an
	// error the last good list is kept.
	Watches func(ctx context.Context) ([]Watch, error)
	// Heartbeat, if set, is called after every read attempt from inside the
	// loop: a hung read means no beat, and wfnexus shows the service silent.
	Heartbeat func(ctx context.Context) error
	StateFile string
	Interval  time.Duration
	Debounce  time.Duration
	HTTP      *http.Client
	Getenv    func(string) string
	Now       func() time.Time
	Logf      func(string, ...any)
}

type state struct {
	Cursor string   `json:"cursor"`
	Seen   []string `json:"seen"`
}

type pending struct {
	events []Event
	last   time.Time
}

// Monitor is the service loop.
type Monitor struct {
	cfg   Config
	mu    sync.Mutex
	st    state
	seen  map[string]bool
	queue map[string]*pending // chat -> held events
	ws    []Watch             // last good watch list
}

const maxSeen = 5000

func New(cfg Config) (*Monitor, error) {
	if len(cfg.Receive) == 0 || cfg.Watches == nil || cfg.StateFile == "" {
		return nil, fmt.Errorf("wamonitor: receive command, watches source and state file are required")
	}
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 3 * time.Second
	}
	if cfg.Channel == "" {
		cfg.Channel = "whatsapp"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Getenv == nil {
		cfg.Getenv = os.Getenv
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	m := &Monitor{cfg: cfg, seen: map[string]bool{}, queue: map[string]*pending{}}
	if raw, err := os.ReadFile(cfg.StateFile); err == nil {
		if err := json.Unmarshal(raw, &m.st); err != nil {
			return nil, fmt.Errorf("wamonitor: state %s: %w", cfg.StateFile, err)
		}
		for _, id := range m.st.Seen {
			m.seen[id] = true
		}
	}
	return m, nil
}

// Run ticks until ctx ends. Polling for debounced batches runs at
// min(Interval, Debounce) so a quiet chat is delivered promptly.
func (m *Monitor) Run(ctx context.Context) error {
	readEvery := m.cfg.Interval
	t := time.NewTicker(minDur(readEvery, m.cfg.Debounce))
	defer t.Stop()
	var lastRead time.Time
	for {
		if m.cfg.Now().Sub(lastRead) >= readEvery {
			if err := m.Read(ctx); err != nil {
				m.cfg.Logf("wamonitor: read: %v", err)
			}
			lastRead = m.cfg.Now()
			if m.cfg.Heartbeat != nil {
				if err := m.cfg.Heartbeat(ctx); err != nil {
					m.cfg.Logf("wamonitor: heartbeat: %v", err)
				}
			}
		}
		m.Flush(ctx, false)
		select {
		case <-ctx.Done():
			m.Flush(context.Background(), true)
			return nil
		case <-t.C:
		}
	}
}

// Read is the one combined read for this tick.
func (m *Monitor) Read(ctx context.Context) error {
	m.refreshWatches(ctx)
	in, _ := json.Marshal(map[string]string{"after": m.st.Cursor})
	cmd := exec.CommandContext(ctx, m.cfg.Receive[0], m.cfg.Receive[1:]...)
	cmd.Env = append(append(os.Environ(), m.cfg.Env...), "APL_WA_GROUP=*")
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var r struct {
		Messages []Event `json:"messages"`
		Cursor   string  `json:"cursor"`
		Notice   string  `json:"notice"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("adapter output is not JSON: %w", err)
	}
	if r.Notice != "" {
		m.cfg.Logf("wamonitor: %s", r.Notice)
	}
	m.mu.Lock()
	now := m.cfg.Now()
	for _, ev := range r.Messages {
		id := ev.str("id")
		key := ev.str("chat") + "|" + id
		if id == "" || m.seen[key] {
			continue
		}
		m.seen[key] = true
		m.st.Seen = append(m.st.Seen, key)
		chat := ev.str("chat")
		p := m.queue[chat]
		if p == nil {
			p = &pending{}
			m.queue[chat] = p
		}
		p.events = append(p.events, ev)
		p.last = now
	}
	if len(m.st.Seen) > maxSeen {
		drop := m.st.Seen[:len(m.st.Seen)-maxSeen]
		for _, k := range drop {
			delete(m.seen, k)
		}
		m.st.Seen = append([]string(nil), m.st.Seen[len(m.st.Seen)-maxSeen:]...)
	}
	if r.Cursor != "" {
		m.st.Cursor = r.Cursor
	}
	err = m.saveLocked()
	m.mu.Unlock()
	return err
}

// Flush delivers every chat that has been quiet for Debounce (all, if force).
func (m *Monitor) Flush(ctx context.Context, force bool) {
	m.mu.Lock()
	watches := m.ws
	now := m.cfg.Now()
	var ready []string
	for chat, p := range m.queue {
		if force || now.Sub(p.last) >= m.cfg.Debounce {
			ready = append(ready, chat)
		}
	}
	sort.Strings(ready)
	batches := map[string][]Event{}
	for _, c := range ready {
		batches[c] = m.queue[c].events
		delete(m.queue, c)
	}
	m.mu.Unlock()

	for _, chat := range ready {
		failed := false
		for _, w := range watches {
			evs := w.match(chat, batches[chat])
			if len(evs) == 0 {
				continue
			}
			if err := m.deliver(ctx, w, chat, evs); err != nil {
				m.cfg.Logf("wamonitor: watch %s: %v (kept for next tick)", w.Name, err)
				failed = true
			}
		}
		if failed {
			// Requeue the whole batch: hooks are idempotent per batch key
			// (Idempotency-Key below), so a watch that already took it
			// answers the first run again instead of starting a second.
			m.mu.Lock()
			p := m.queue[chat]
			if p == nil {
				p = &pending{}
				m.queue[chat] = p
			}
			p.events = append(batches[chat], p.events...)
			p.last = now
			m.mu.Unlock()
		}
	}
}

func (w Watch) match(chat string, evs []Event) []Event {
	if w.Chat != "*" && w.Chat != chat {
		return nil
	}
	kinds := w.Kinds
	if len(kinds) == 0 {
		kinds = []string{"message"}
	}
	var out []Event
	for _, e := range evs {
		k := e.str("kind")
		if k == "" {
			k = "message"
		}
		for _, want := range kinds {
			if want == k {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func (m *Monitor) deliver(ctx context.Context, w Watch, chat string, evs []Event) error {
	token := m.cfg.Getenv(w.TokenEnv)
	if w.TokenEnv == "" || token == "" {
		return fmt.Errorf("no hook token: set the env var named by tokenEnv (%q)", w.TokenEnv)
	}
	stamped := make([]Event, len(evs))
	for i, e := range evs {
		c := Event{}
		for k, v := range e {
			c[k] = v
		}
		if c.str("channel") == "" {
			c["channel"] = m.cfg.Channel
		}
		if c.str("chat") == "" {
			c["chat"] = chat
		}
		stamped[i] = c
	}
	body, _ := json.Marshal(map[string]any{
		"message":  stamped[len(stamped)-1], // the newest: what a one-message workflow reads
		"messages": stamped,                 // the whole debounced batch, oldest first
		"watch": map[string]any{"name": w.Name, "responsePolicy": w.ResponsePolicy,
			"instructions": w.Instructions, "skills": w.Skills},
		"chat": chat,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", w.Hook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "wa-"+w.Name+"-"+chat+"-"+evs[len(evs)-1].str("id"))
	resp, err := m.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("hook answered %s", resp.Status)
	}
	return nil
}

func (m *Monitor) refreshWatches(ctx context.Context) {
	ws, err := m.cfg.Watches(ctx)
	if err == nil {
		for _, w := range ws {
			if w.Name == "" || w.Chat == "" || w.Hook == "" {
				err = fmt.Errorf("watch %q needs name, chat and hook", w.Name)
				break
			}
		}
	}
	if err != nil {
		m.cfg.Logf("wamonitor: watches: %v (keeping the last list)", err)
		return
	}
	m.mu.Lock()
	m.ws = ws
	m.mu.Unlock()
}

func (m *Monitor) saveLocked() error {
	b, _ := json.Marshal(m.st)
	if err := os.MkdirAll(filepath.Dir(m.cfg.StateFile), 0o700); err != nil {
		return err
	}
	tmp := m.cfg.StateFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.cfg.StateFile)
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
