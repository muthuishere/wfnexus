package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
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

// W3 done-when (in): a forged or replayed hook is refused.
func TestSignedHookRefusesForgedStaleAndReplayed(t *testing.T) {
	f := hostedServer(t, "127.0.0.1:0")
	if code, out := f.do(t, "POST", "/api/projects", map[string]any{"name": "acme", "create": true}, nil); code != 201 {
		t.Fatalf("create = %d %v", code, out)
	}
	f.addWorkflow(t, "acme", "ok.yaml", okFlow)
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
	// The same request again — a replay inside the window — is refused.
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
	// A different body under a good signature is forged too.
	if code, _ := f.do(t, "POST", hook, []byte(`{"x":2}`), headers(secret, "evt-9", now, body)); code != http.StatusUnauthorized {
		t.Errorf("body swapped: %d, want 401", code)
	}
}

type receivedEvent struct {
	ID, Type, RunID string
	SigOK           bool
}

// W3 done-when (out): the receiver gets each run event once, with a valid
// signature — even when its first answer is a 500.
func TestRunEventsDeliveredOnceSigned(t *testing.T) {
	oldBase, oldPoll := eventRetryBase, eventPollEvery
	eventRetryBase, eventPollEvery = 20*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { eventRetryBase, eventPollEvery = oldBase, oldPoll })

	var mu sync.Mutex
	var secret string
	failedOnce := false
	accepted := map[string]int{}
	var got []receivedEvent
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if !failedOnce {
			failedOnce = true
			w.WriteHeader(500) // the first delivery fails: it must be retried
			return
		}
		id := r.Header.Get(hookEventIDName)
		ts, _ := strconv.ParseInt(r.Header.Get(hookTimestampName), 10, 64)
		var ev map[string]any
		_ = json.Unmarshal(body, &ev)
		sigOK := r.Header.Get(hookSignatureName) == SignPayload(secret, ts, id, body) && ev["id"] == id
		accepted[id]++
		got = append(got, receivedEvent{ID: id, Type: ev["type"].(string), RunID: ev["runId"].(string), SigOK: sigOK})
		w.WriteHeader(204)
	}))
	t.Cleanup(recv.Close)

	f := hostedServer(t, "127.0.0.1:0")
	for _, p := range []string{"acme", "globex"} {
		if code, out := f.do(t, "POST", "/api/projects", map[string]any{"name": p, "create": true}, nil); code != 201 {
			t.Fatalf("create = %d %v", code, out)
		}
	}
	f.addWorkflow(t, "acme", "ok.yaml", okFlow)
	f.addWorkflow(t, "acme", "boom.yaml", failFlow)
	f.addWorkflow(t, "acme", "gate.yaml", gateFlow)
	f.addWorkflow(t, "globex", "ok.yaml", okFlow)
	code, out := f.do(t, "PUT", "/api/projects/acme/events", map[string]any{"url": recv.URL}, nil)
	if code != 200 {
		t.Fatalf("set events = %d %v", code, out)
	}
	mu.Lock()
	secret = out["signingSecret"].(string)
	mu.Unlock()

	runs := map[string]string{}
	for _, wf := range []string{"acme%2Fok", "acme%2Fboom", "acme%2Fgate", "globex%2Fok"} {
		code, out := f.do(t, "POST", "/api/workflows/"+wf+"/runs", map[string]any{}, nil)
		if code != 201 {
			t.Fatalf("run %s = %d %v", wf, code, out)
		}
		runs[wf] = out["run"].(map[string]any)["id"].(string)
		f.waitRun(t, runs[wf])
	}
	want := map[string]string{
		runs["acme%2Fok"] + ".started":                     "run.started",
		runs["acme%2Fok"] + ".finished":                    "run.finished",
		runs["acme%2Fboom"] + ".started":                   "run.started",
		runs["acme%2Fboom"] + ".failed":                    "run.failed",
		runs["acme%2Fgate"] + ".waiting_for_approval.ship": "run.waiting_for_approval",
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := 0
		for id := range want {
			if accepted[id] > 0 {
				n++
			}
		}
		mu.Unlock()
		if n == len(want) {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("delivered %v, want %v", accepted, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // any duplicate would land in this window
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range got {
		if !ev.SigOK {
			t.Errorf("event %s: bad signature", ev.ID)
		}
		if ev.RunID == runs["globex%2Fok"] {
			t.Errorf("globex has no event URL, yet its run %s was posted to acme's", ev.ID)
		}
		if typ, ok := want[ev.ID]; ok && typ != ev.Type {
			t.Errorf("event %s type %s, want %s", ev.ID, ev.Type, typ)
		}
	}
	for id := range want {
		if accepted[id] != 1 {
			t.Errorf("event %s accepted %d times, want once", id, accepted[id])
		}
	}
}
