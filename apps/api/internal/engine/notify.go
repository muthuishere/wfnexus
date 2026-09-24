package engine

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
)

// notifiers builds the delivery adapters from the registry, fresh each pause so
// an edit to registries.json takes effect without a restart. A notifier being
// ABSENT is not an error: the one-person scale must stay zero-config (ADR 0021).
func (e *Engine) notifiers() []notify.Notifier {
	e.mu.Lock()
	cat := e.catalog
	e.mu.Unlock()
	if cat == nil || cat.Notifiers == nil {
		return nil
	}
	var out []notify.Notifier
	for _, n := range cat.Notifiers.List() {
		if n.Kind != "webhook" {
			continue
		}
		out = append(out, notify.Webhook{
			EntryName: n.Name, URL: n.URL, Headers: n.Headers, SecretEnv: n.SecretEnv,
			Client: &http.Client{Timeout: 10 * time.Second, Transport: e.transport},
		})
	}
	return out
}

// pauseURL is where a PERSON goes to resolve this pause. It is a pointer at our
// own UI, which authenticates; it is deliberately not a resolve-on-click link,
// because then possession of the chat channel would be the approval (ADR 0021).
func (e *Engine) pauseURL(runID uuid.UUID) string {
	base := strings.TrimRight(e.cfg.PublicURL, "/")
	if base == "" {
		addr := e.cfg.Addr
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		base = "http://" + addr
	}
	return base + "/runs/" + runID.String()
}

// notifyPause announces a halted run on every configured channel, and records
// the delivery on the run's own event stream so "nobody was told" is visible.
//
// A pause that cannot be delivered is STILL a pause: the run has already parked
// by the time this runs, and a failure here never changes that.
func (e *Engine) notifyPause(ctx context.Context, runID uuid.UUID, stepID, kind, prompt string, req *tn.Request) {
	ns := e.notifiers()
	if len(ns) == 0 {
		return
	}
	p := notify.Pause{
		Kind: kind, RunID: runID.String(),
		StepID: stepID, Prompt: prompt, URL: e.pauseURL(runID), At: time.Now().UTC(),
	}
	// The workflow and project come off the RUN, not the caller's template
	// data: one source, and every pause names itself the same way.
	if run, err := e.store.GetRun(ctx, runID); err == nil && run != nil {
		p.Workflow, p.Project = run.Workflow, run.Project
	}
	if req != nil {
		p.RequestID = req.ID
		if req.URL != "" {
			p.URL = req.URL
		}
		// Request.ExpiresAt is an RFC3339 STRING on the wire; an unparseable
		// one is dropped rather than guessed at.
		if t, err := time.Parse(time.RFC3339, req.ExpiresAt); req.ExpiresAt != "" && err == nil {
			p.ExpiresAt = &t
		}
	}
	for _, n := range ns {
		err := n.Notify(ctx, p)
		if err != nil {
			log.Printf("engine: notifier %s: %v", n.Name(), err)
			e.emit(ctx, runID, stepID, "log", map[string]any{
				"text": "notifier " + n.Name() + " failed: " + err.Error() + " (the run is still paused)",
			})
			continue
		}
		e.emit(ctx, runID, stepID, "log", map[string]any{
			"text": "pause (" + kind + ") announced on notifier " + n.Name() + " → " + p.URL,
		})
	}
}
