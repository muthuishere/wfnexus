// Package notify delivers a PAUSE to wherever a person is (ADR 0021).
//
// ONE interface, one method, one shipped adapter — a webhook. Slack, Telegram
// and email are registry entries somebody writes against that webhook, not Go
// we compile. That is deliberate: docs/research/competitors-2026-09.md records
// gh-aw's 40+ `safe-outputs` handlers, where "every capability needs a compiler
// change", as the mistake to avoid.
//
// SECURITY, and it is the whole point of the shape: a notification is NOT an
// authorization path. The payload carries a POINTER to the pause — run id, step
// id, the question, and the URL of the page where it is answered — and never a
// token, a signed link or anything else that resolves it. Possession of the
// channel must never be sufficient to approve; the answer is authenticated on
// our own API. A notifier that could carry a capability would make the approval
// control worth exactly as much as the membership list of a chat workspace we
// do not administer.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Pause is everything a channel needs to say "somebody has to look at this",
// and nothing it needs to act on the person's behalf.
type Pause struct {
	// Kind is the toolnexus Request vocabulary: approval | input.
	Kind     string `json:"kind"`
	RunID    string `json:"runId"`
	Workflow string `json:"workflow"`
	Project  string `json:"project,omitempty"`
	StepID   string `json:"stepId"`
	// Prompt is the question or the approval text, verbatim from Request.Prompt.
	Prompt string `json:"prompt"`
	// URL is where a HUMAN goes to resolve it — Request.URL is literally this
	// field. It is a pointer at our UI, which authenticates; it is never a
	// resolve-on-click capability.
	URL       string     `json:"url,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	RequestID string     `json:"requestId,omitempty"`
	At        time.Time  `json:"at"`
}

// Notifier delivers a pause. One method, on purpose.
type Notifier interface {
	Name() string
	Notify(ctx context.Context, p Pause) error
}

// Webhook POSTs the Pause as JSON. The only adapter in the tree.
type Webhook struct {
	EntryName string
	URL       string
	Headers   map[string]string
	// SecretEnv names an env var whose value is sent as a bearer token so the
	// RECEIVER can authenticate US. It never authenticates a person back to us.
	SecretEnv string
	Client    *http.Client
}

func (w Webhook) Name() string { return w.EntryName }

func (w Webhook) Notify(ctx context.Context, p Pause) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}
	if w.SecretEnv != "" {
		if tok := os.Getenv(w.SecretEnv); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	cl := w.Client
	if cl == nil {
		cl = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("notifier %s: %s returned %s", w.EntryName, w.URL, res.Status)
	}
	return nil
}
