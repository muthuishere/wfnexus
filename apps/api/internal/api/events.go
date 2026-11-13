package api

// Run events OUT. A project names one URL:
//
//	PUT    /api/projects/{name}/events {url}  → {url, signingSecret} (secret shown once)
//	GET    /api/projects/{name}/events        → {url, configured}
//	DELETE /api/projects/{name}/events
//
// Every run of the project then posts run.started, run.waiting_for_approval,
// run.finished and run.failed to it. Each post carries
//
//	X-WFX-Event-Id     stable for the event across every delivery attempt —
//	                   dedupe on it. `<run>.<transition>[.<step>].<n>`, where
//	                   n counts occurrences: a retried run starts (and fails,
//	                   or waits) again, and each time is a new event.
//	X-WFX-Delivery-Id  unique per delivery attempt (`<event id>/<attempt>`)
//	X-WFX-Timestamp    unix seconds
//	X-WFX-Signature    SignPayload(secret, timestamp, event id, body)
//
// — the same scheme a signed hook IN uses. Delivery is at least once: the
// event is written to an outbox when the status lands and retried with
// backoff until the receiver answers 2xx. Each target URL gets its own
// timeout and at most eventsPerTarget posts in flight, so one slow receiver
// delays only its own events.

import (
	"bytes"
	"context"
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

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

var (
	// eventRetryBase is the first retry delay; each failure doubles it, up to
	// eventRetryMax. Vars so tests can shorten them.
	eventRetryBase = 2 * time.Second
	eventRetryMax  = 10 * time.Minute
	// eventPollEvery is how often the loop looks for due events when nothing
	// has kicked it.
	eventPollEvery = 2 * time.Second
	// eventTimeout bounds one post to one target.
	eventTimeout = 3 * time.Second
	// eventsPerTarget bounds the posts in flight to one URL.
	eventsPerTarget = 2
)

// eventMaxAttempts: after this many failures an event is given up on.
const eventMaxAttempts = 20

const deliveryIDName = "X-WFX-Delivery-Id"

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

	mu sync.Mutex
	// inflight counts posts in progress per target URL.
	inflight map[string]int

	// The knobs above, read once when the server is built.
	retryBase, retryMax, poll, timeout time.Duration
	perTarget                          int
}

func newEventLoop() *eventLoop {
	return &eventLoop{kick: make(chan struct{}, 1), http: &http.Client{}, inflight: map[string]int{},
		retryBase: eventRetryBase, retryMax: eventRetryMax, poll: eventPollEvery, timeout: eventTimeout, perTarget: eventsPerTarget}
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
	if typ == "run.started" {
		// A run goes back to `running` after every approval; that is the same
		// attempt continuing, not a new start. It starts when nothing was
		// sent yet, or after it last ended (a retry).
		last, err := s.store.LastRunEventType(ctx, runID.String())
		if err != nil || (last != "" && last != "run.finished" && last != "run.failed") {
			return
		}
	}
	stem := runID.String() + "." + strings.TrimPrefix(typ, "run.")
	if typ == "run.waiting_for_approval" {
		stem += "." + step
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	id, err := s.store.EnqueueEvent(ctx, stem, runID.String(), run.Project, typ, func(id string) string {
		b, _ := json.Marshal(map[string]any{
			"id": id, "type": typ, "project": run.Project, "workflow": run.Workflow,
			"runId": runID.String(), "status": status, "step": step, "error": errMsg, "time": at,
		})
		return string(b)
	})
	if err != nil {
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
	t := time.NewTicker(s.events.poll)
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

// deliverDue starts a post for every due event whose target has room. It
// does not wait for them: a slow target holds only its own slots.
func (s *Server) deliverDue(ctx context.Context) error {
	due, err := s.store.DueEvents(ctx, 200)
	if err != nil {
		return err
	}
	targets := map[string]string{} // project → url, once per pass
	for _, d := range due {
		target, ok := targets[d.Project]
		if !ok {
			target, _, _ = s.store.ProjectEvents(ctx, d.Project)
			targets[d.Project] = target
		}
		s.events.mu.Lock()
		full := s.events.inflight[target] >= s.events.perTarget
		if !full {
			s.events.inflight[target]++
		}
		s.events.mu.Unlock()
		if full {
			continue
		}
		// Leased for longer than one post can take, so another instance's
		// loop leaves it alone while this one tries.
		if ok, err := s.store.ClaimEvent(ctx, d.EventID, d.NextNs, time.Now().Add(s.events.timeout+time.Minute)); err != nil || !ok {
			s.release(target)
			continue
		}
		go func(d store.Delivery, target string) {
			defer s.release(target)
			if err := s.postEvent(ctx, d.Project, d.EventID, d.Attempts+1, []byte(d.Body)); err != nil {
				wait := s.events.retryBase << min(d.Attempts, 20)
				if wait > s.events.retryMax || wait <= 0 {
					wait = s.events.retryMax
				}
				_ = s.store.EventFailed(ctx, d.EventID, err.Error(), time.Now().Add(wait), d.Attempts+1 >= eventMaxAttempts)
				return
			}
			_ = s.store.EventDelivered(ctx, d.EventID)
			s.kickEvents() // a freed slot may have work waiting
		}(d, target)
	}
	return nil
}

func (s *Server) release(target string) {
	s.events.mu.Lock()
	s.events.inflight[target]--
	if s.events.inflight[target] <= 0 {
		delete(s.events.inflight, target)
	}
	s.events.mu.Unlock()
}

func (s *Server) postEvent(ctx context.Context, project, id string, attempt int, body []byte) error {
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
	ctx, cancel := context.WithTimeout(ctx, s.events.timeout)
	defer cancel()
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(hookEventIDName, id)
	req.Header.Set(deliveryIDName, id+"/"+strconv.Itoa(attempt))
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

// eventsProject: the project exists and is within the caller's scope.
func (s *Server) eventsProject(r *http.Request, name string) bool {
	if !s.inScope(r, name) {
		return false
	}
	_, err := s.eng.Project(r.Context(), name)
	return err == nil
}

func (s *Server) getProjectEvents(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.eventsProject(r, name) {
		notFound(w)
		return
	}
	u, _, err := s.store.ProjectEvents(r.Context(), name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"url": u, "configured": u != ""})
}

func (s *Server) setProjectEvents(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.eventsProject(r, name) {
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
	if !s.eventsProject(r, name) {
		notFound(w)
		return
	}
	if err := s.store.SetProjectEvents(r.Context(), name, "", nil); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
