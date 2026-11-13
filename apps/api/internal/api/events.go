package api

// Run events OUT (plan 18, W3). A project names one URL:
//
//	PUT    /api/projects/{name}/events {url}  → {url, signingSecret} (secret shown once)
//	GET    /api/projects/{name}/events        → {url, configured}
//	DELETE /api/projects/{name}/events
//
// Every run of the project then posts run.started, run.waiting_for_approval,
// run.finished and run.failed to it. Each post carries
//
//	X-WFX-Event-Id   deterministic per run and transition — dedupe on it
//	X-WFX-Timestamp  unix seconds
//	X-WFX-Signature  SignPayload(secret, timestamp, event id, body)
//
// — the same scheme a signed hook IN uses. Delivery is at least once: the
// event is written to an outbox when the status lands and retried with
// backoff until the receiver answers 2xx.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// eventRetryBase is the first retry delay; each failure doubles it, up to
	// eventRetryMax. Vars so tests can shorten them.
	eventRetryBase = 2 * time.Second
	eventRetryMax  = 10 * time.Minute
	// eventPollEvery is how often the loop looks for due events when nothing
	// has kicked it.
	eventPollEvery = 2 * time.Second
)

// eventMaxAttempts: after this many failures an event is given up on.
const eventMaxAttempts = 20

// eventTypes maps a run status to the event it emits.
var eventTypes = map[string]string{
	"running":           "run.started",
	"awaiting_approval": "run.waiting_for_approval",
	"done":              "run.finished",
	"failed":            "run.failed",
}

type eventLoop struct {
	once sync.Once
	kick chan struct{}
	http *http.Client
}

func newEventLoop() *eventLoop {
	return &eventLoop{kick: make(chan struct{}, 1), http: &http.Client{Timeout: 10 * time.Second}}
}

// recordRunEvent is the engine's OnRunStatus listener: it writes the event to
// the outbox when the run's project has an event URL.
func (s *Server) recordRunEvent(ctx context.Context, runID uuid.UUID, status, step, errMsg string) {
	typ, ok := eventTypes[status]
	if !ok {
		return
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	u, _, err := s.store.ProjectEvents(ctx, run.Project)
	if err != nil || u == "" {
		return
	}
	// One id per run and transition. A run resumes to `running` after every
	// approval, but it STARTED once; it may wait at several steps, each once.
	id := runID.String() + "." + strings.TrimPrefix(typ, "run.")
	if typ == "run.waiting_for_approval" {
		id += "." + step
	}
	body, _ := json.Marshal(map[string]any{
		"id": id, "type": typ, "project": run.Project, "workflow": run.Workflow,
		"runId": runID.String(), "status": status, "step": step, "error": errMsg,
		"time": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err := s.store.EnqueueEvent(ctx, id, run.Project, typ, string(body)); err != nil {
		log.Printf("events: queueing %s: %v", id, err)
		return
	}
	s.kickEvents()
}

func (s *Server) kickEvents() {
	s.events.once.Do(func() { go s.deliverLoop() })
	select {
	case s.events.kick <- struct{}{}:
	default:
	}
}

func (s *Server) deliverLoop() {
	t := time.NewTicker(eventPollEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.events.kick:
		}
		if err := s.deliverDue(context.Background()); err != nil {
			if strings.Contains(err.Error(), "database is closed") {
				return
			}
			log.Printf("events: %v", err)
		}
	}
}

// deliverDue posts every due event once.
func (s *Server) deliverDue(ctx context.Context) error {
	due, err := s.store.DueEvents(ctx, 100)
	if err != nil {
		return err
	}
	for _, d := range due {
		// Leased for longer than one post can take, so another instance's
		// loop leaves it alone while this one tries.
		if ok, err := s.store.ClaimEvent(ctx, d.EventID, d.NextNs, time.Now().Add(time.Minute)); err != nil || !ok {
			continue
		}
		if err := s.postEvent(ctx, d.Project, d.EventID, []byte(d.Body)); err != nil {
			wait := eventRetryBase << min(d.Attempts, 20)
			if wait > eventRetryMax || wait <= 0 {
				wait = eventRetryMax
			}
			dead := d.Attempts+1 >= eventMaxAttempts
			_ = s.store.EventFailed(ctx, d.EventID, err.Error(), time.Now().Add(wait), dead)
			continue
		}
		_ = s.store.EventDelivered(ctx, d.EventID)
	}
	return nil
}

func (s *Server) postEvent(ctx context.Context, project, id string, body []byte) error {
	u, sealed, err := s.store.ProjectEvents(ctx, project)
	if err != nil || u == "" {
		return fmt.Errorf("no event URL for %s", project)
	}
	box := s.eng.Secrets()
	if box == nil {
		return errors.New("no server key to open the signing secret")
	}
	secret, err := box.Open(sealed)
	if err != nil {
		return err
	}
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(hookEventIDName, id)
	req.Header.Set(hookTimestampName, strconv.FormatInt(ts, 10))
	req.Header.Set(hookSignatureName, SignPayload(secret, ts, id, body))
	resp, err := s.events.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("receiver answered %d", resp.StatusCode)
	}
	return nil
}

// ---- configuration routes ----

func (s *Server) getProjectEvents(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	u, _, err := s.store.ProjectEvents(r.Context(), name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"url": u, "configured": u != ""})
}

func (s *Server) setProjectEvents(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	pu, err := url.Parse(body.URL)
	if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
		writeErr(w, 400, errors.New("url must be an http(s) URL"))
		return
	}
	box := s.eng.Secrets()
	if box == nil {
		writeErr(w, 400, errors.New("this server has no secret key, so it cannot keep a signing secret"))
		return
	}
	secret, err := newSigningSecret()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	sealed, err := box.Seal(secret)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := s.store.SetProjectEvents(r.Context(), name, body.URL, sealed); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"url": body.URL, "signingSecret": secret})
}

func (s *Server) deleteProjectEvents(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	if err := s.store.SetProjectEvents(r.Context(), name, "", nil); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
