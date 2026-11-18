package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

const okFlow = `name: ok
steps:
  - id: a
    run: echo ok
`

const failFlow = `name: boom
steps:
  - id: a
    run: exit 3
`

const gateFlow = `name: gate
steps:
  - id: ship
    requires_approval: true
    run: echo shipped
`

// eventsFixture: a loopback server with projects imported from temp repos.
type eventsFixture struct {
	h     http.Handler
	st    *store.Store
	eng   *engine.Engine
	repos map[string]string
}

func eventsServer(t *testing.T, projects map[string]map[string]string) eventsFixture {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "")
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cfg := config.Config{Addr: "127.0.0.1:0", WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(), MaxConcurrentRuns: 4,
		SecretKeyEnv: "WFX_EVENTS_TEST_NO_SUCH_KEY", SecretKeyPath: filepath.Join(t.TempDir(), "secret.key")}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	f := eventsFixture{st: st, eng: eng, repos: map[string]string{}}
	for p, files := range projects {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".wfx", "workflows")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := eng.ImportRepo(context.Background(), p, repo, ""); err != nil {
			t.Fatal(err)
		}
		f.repos[p] = repo
	}
	f.h = New(eng, st, nil, cfg.Addr, "", nil)
	return f
}

func (f eventsFixture) do(t *testing.T, method, path string, body any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		raw, _ = json.Marshal(b)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f eventsFixture) run(t *testing.T, wf string) string {
	t.Helper()
	code, out := f.do(t, "POST", "/api/workflows/"+wf+"/runs", map[string]any{}, nil)
	if code != 201 {
		t.Fatalf("run %s = %d %v", wf, code, out)
	}
	id := out["run"].(map[string]any)["id"].(string)
	f.settle(t, id)
	return id
}

func (f eventsFixture) settle(t *testing.T, id string) *store.Run {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		run, err := f.st.GetRun(context.Background(), uuid.MustParse(id))
		if err != nil {
			t.Fatal(err)
		}
		switch run.Status {
		case "done", "failed", "cancelled", "awaiting_approval":
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s still %s", id, run.Status)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func fastEvents(t *testing.T) {
	oldBase, oldPoll, oldTO := eventRetryBase, eventPollEvery, eventTimeout
	eventRetryBase, eventPollEvery = 20*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { eventRetryBase, eventPollEvery, eventTimeout = oldBase, oldPoll, oldTO })
}

// receiver records accepted events; failFirst answers 500 to the first post.
type receiver struct {
	mu        sync.Mutex
	secret    string
	failFirst bool
	failed    bool
	delay     time.Duration
	accepted  map[string]int // event id → accepted posts
	delivery  map[string]bool
	types     map[string]string
	runs      map[string]string
	badSig    []string
	inflight  int
	maxFlight int
	srv       *httptest.Server
}

func newReceiver(t *testing.T, failFirst bool, delay time.Duration) *receiver {
	rc := &receiver{failFirst: failFirst, delay: delay, accepted: map[string]int{}, delivery: map[string]bool{},
		types: map[string]string{}, runs: map[string]string{}}
	rc.srv = httptest.NewServer(http.HandlerFunc(rc.serve))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	rc.inflight++
	if rc.inflight > rc.maxFlight {
		rc.maxFlight = rc.inflight
	}
	rc.mu.Unlock()
	defer func() { rc.mu.Lock(); rc.inflight--; rc.mu.Unlock() }()
	if rc.delay > 0 {
		select {
		case <-time.After(rc.delay):
		case <-r.Context().Done():
			return
		}
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	did := r.Header.Get(deliveryIDName)
	if rc.delivery[did] {
		rc.badSig = append(rc.badSig, "delivery id reused: "+did)
	}
	rc.delivery[did] = true
	if rc.failFirst && !rc.failed {
		rc.failed = true
		w.WriteHeader(500)
		return
	}
	id := r.Header.Get(hookEventIDName)
	ts, _ := strconv.ParseInt(r.Header.Get(hookTimestampName), 10, 64)
	var ev map[string]any
	_ = json.Unmarshal(body, &ev)
	if r.Header.Get(hookSignatureName) != SignPayload(rc.secret, ts, id, body) || ev["id"] != id || !strings.HasPrefix(did, id+"/") {
		rc.badSig = append(rc.badSig, id)
	}
	rc.accepted[id]++
	rc.types[id], _ = ev["type"].(string)
	rc.runs[id], _ = ev["runId"].(string)
	w.WriteHeader(204)
}

func (rc *receiver) waitFor(t *testing.T, ids ...string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		rc.mu.Lock()
		n := 0
		for _, id := range ids {
			if rc.accepted[id] > 0 {
				n++
			}
		}
		rc.mu.Unlock()
		if n == len(ids) {
			return
		}
		if time.Now().After(deadline) {
			rc.mu.Lock()
			defer rc.mu.Unlock()
			t.Fatalf("delivered %v, want %v", rc.accepted, ids)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f eventsFixture) subscribe(t *testing.T, project string, rc *receiver) {
	t.Helper()
	code, out := f.do(t, "PUT", "/api/projects/"+project+"/events", map[string]any{"url": rc.srv.URL}, nil)
	if code != 200 {
		t.Fatalf("set events = %d %v", code, out)
	}
	rc.mu.Lock()
	rc.secret = out["signingSecret"].(string)
	rc.mu.Unlock()
}

// A signed hook: forged, stale or replayed is refused.
func TestSignedHookRefusesForgedStaleAndReplayed(t *testing.T) {
	f := eventsServer(t, map[string]map[string]string{"acme": {"ok.yaml": okFlow}})
	code, out := f.do(t, "POST", "/api/workflows/acme%2Fok/expose/hook", map[string]any{}, nil)
	secret, _ := out["signingSecret"].(string)
	if code != 201 || secret == "" {
		t.Fatalf("issue hook = %d %v", code, out)
	}
	hook := "/hooks/acme/ok"
	body := []byte(`{"x":1}`)
	headers := func(key, id string, ts int64, b []byte) map[string]string {
		return map[string]string{hookSignatureName: SignPayload(key, ts, id, b),
			hookTimestampName: strconv.FormatInt(ts, 10), hookEventIDName: id}
	}
	now := time.Now().Unix()
	good := headers(secret, "evt-1", now, body)
	if code, out := f.do(t, "POST", hook, body, good); code != http.StatusAccepted {
		t.Fatalf("signed hook = %d %v", code, out)
	}
	if code, out := f.do(t, "POST", hook, body, good); code != http.StatusConflict || out["runId"] == "" {
		t.Fatalf("replayed hook = %d %v, want 409 naming the first run", code, out)
	}
	refused := map[string]map[string]string{
		"wrong secret":     headers("wfxs_not-the-secret", "evt-2", now, body),
		"stale timestamp":  headers(secret, "evt-3", now-int64((10*time.Minute).Seconds()), body),
		"future timestamp": headers(secret, "evt-4", now+int64((10*time.Minute).Seconds()), body),
		"no event id":      {hookSignatureName: SignPayload(secret, now, "", body), hookTimestampName: strconv.FormatInt(now, 10)},
		"no timestamp":     {hookSignatureName: SignPayload(secret, now, "evt-5", body), hookEventIDName: "evt-5"},
		"event id swapped": func() map[string]string {
			h := headers(secret, "evt-6", now, body)
			h[hookEventIDName] = "evt-7"
			return h
		}(),
		"timestamp swapped": func() map[string]string {
			h := headers(secret, "evt-8", now, body)
			h[hookTimestampName] = strconv.FormatInt(now+1, 10)
			return h
		}(),
	}
	for why, h := range refused {
		if code, _ := f.do(t, "POST", hook, body, h); code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", why, code)
		}
	}
	if code, _ := f.do(t, "POST", hook, []byte(`{"x":2}`), headers(secret, "evt-9", now, body)); code != http.StatusUnauthorized {
		t.Errorf("body swapped: %d, want 401", code)
	}
}

// Each run event is accepted once, with a valid signature, even when the
// receiver's first answer is a 500; every attempt has its own delivery id.
func TestRunEventsDeliveredOnceSigned(t *testing.T) {
	fastEvents(t)
	rc := newReceiver(t, true, 0)
	f := eventsServer(t, map[string]map[string]string{
		"acme":   {"ok.yaml": okFlow, "boom.yaml": failFlow, "gate.yaml": gateFlow},
		"globex": {"ok.yaml": okFlow},
	})
	f.subscribe(t, "acme", rc)
	ok, boom, gate, other := f.run(t, "acme%2Fok"), f.run(t, "acme%2Fboom"), f.run(t, "acme%2Fgate"), f.run(t, "globex%2Fok")
	want := map[string]string{
		ok + ".started.1": "run.started", ok + ".finished.1": "run.finished",
		boom + ".started.1": "run.started", boom + ".failed.1": "run.failed",
		gate + ".waiting_for_approval.ship.1": "run.waiting_for_approval",
	}
	var ids []string
	for id := range want {
		ids = append(ids, id)
	}
	rc.waitFor(t, ids...)
	time.Sleep(200 * time.Millisecond) // any duplicate would land in this window
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.badSig) > 0 {
		t.Errorf("bad deliveries: %v", rc.badSig)
	}
	for id, typ := range want {
		if rc.accepted[id] != 1 || rc.types[id] != typ {
			t.Errorf("event %s: accepted %d as %q, want once as %s", id, rc.accepted[id], rc.types[id], typ)
		}
	}
	for id, run := range rc.runs {
		if run == other {
			t.Errorf("globex has no event URL, yet %s was posted", id)
		}
	}
	if len(rc.accepted) != len(want) {
		t.Errorf("accepted %v, want exactly %v", rc.accepted, want)
	}
}

// A retried run starts and fails again: those are new events (new ids), not
// duplicates of the first attempt's.
func TestRetriedRunSendsNewEvents(t *testing.T) {
	fastEvents(t)
	rc := newReceiver(t, false, 0)
	f := eventsServer(t, map[string]map[string]string{"acme": {"boom.yaml": failFlow}})
	f.subscribe(t, "acme", rc)
	id := f.run(t, "acme%2Fboom")
	rc.waitFor(t, id+".started.1", id+".failed.1")
	if code, out := f.do(t, "POST", "/api/runs/"+id+"/retry", map[string]any{"stepId": "a"}, nil); code != 200 {
		t.Fatalf("retry = %d %v", code, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if r := f.settle(t, id); r.Status == "failed" {
			rc.mu.Lock()
			n := rc.accepted[id+".failed.2"]
			rc.mu.Unlock()
			if n > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	rc.waitFor(t, id+".started.2", id+".failed.2")
}

// A slow target does not hold up another target's events, and never has
// more than eventsPerTarget posts in flight.
func TestSlowTargetDoesNotDelayOthers(t *testing.T) {
	fastEvents(t)
	eventTimeout = 400 * time.Millisecond
	slow := newReceiver(t, false, 5*time.Second) // never answers in time
	fast := newReceiver(t, false, 0)
	f := eventsServer(t, map[string]map[string]string{
		"slowco": {"ok.yaml": okFlow},
		"fastco": {"ok.yaml": okFlow},
	})
	f.subscribe(t, "slowco", slow)
	f.subscribe(t, "fastco", fast)
	for i := 0; i < 4; i++ {
		f.run(t, "slowco%2Fok") // 8 events queued for the slow target
	}
	start := time.Now()
	id := f.run(t, "fastco%2Fok")
	fast.waitFor(t, id+".started.1", id+".finished.1")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the fast target waited %v behind the slow one", d)
	}
	time.Sleep(time.Second) // several timeouts and retries on the slow target
	slow.mu.Lock()
	defer slow.mu.Unlock()
	if slow.maxFlight > eventsPerTarget {
		t.Fatalf("slow target had %d posts in flight, bound is %d", slow.maxFlight, eventsPerTarget)
	}
	if slow.maxFlight == 0 {
		t.Fatal("the slow target was never tried")
	}
}
