package wamonitor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type row map[string]any

func msg(chat, id, ts, text string, fromMe bool) row {
	return row{"ChatJID": chat, "MsgID": id, "SenderJID": "111@s.whatsapp.net", "Timestamp": ts, "Text": text, "FromMe": fromMe}
}

type hit struct {
	path, auth, key string
	body            map[string]any
}

// harness: the REAL adapter (adapters/apl-whatsapp.js) over a fake apl.
type harness struct {
	t       *testing.T
	store   string
	ledger  string
	m       *Monitor
	now     time.Time
	mu      sync.Mutex
	hits    []hit
	failing bool
	ws      []Watch // what wfnexus answers for this service's watches
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	h := &harness{t: t, store: filepath.Join(dir, "store.json"), ledger: filepath.Join(dir, "sent.jsonl"),
		now: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)}
	h.put()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.failing {
			w.WriteHeader(500)
			return
		}
		h.hits = append(h.hits, hit{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), body})
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)
	watches, _ := json.Marshal([]Watch{
		{Name: "ops", Chat: "ops@g.us", Hook: srv.URL + "/hooks/p/ops-bot", TokenEnv: "OPS_HOOK"},
		{Name: "all", Chat: "*", Kinds: []string{"message", "reaction"}, Hook: srv.URL + "/hooks/p/archive", TokenEnv: "ALL_HOOK"},
	})
	_ = json.Unmarshal(watches, &h.ws)
	adapter, _ := filepath.Abs("../../../../adapters/apl-whatsapp.js")
	fake, _ := filepath.Abs("testdata/fake-apl.js")
	m, err := New(Config{
		Receive: []string{"node", adapter, "receive"},
		Env:     []string{"APL_BIN=" + fake, "APL_WA_LEDGER=" + h.ledger, "FAKE_WA_STORE=" + h.store},
		Watches: func(context.Context) ([]Watch, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]Watch(nil), h.ws...), nil
		},
		StateFile: filepath.Join(dir, "state.json"),
		Debounce:  3 * time.Second,
		Getenv:    envMap{"OPS_HOOK": "tok-ops", "ALL_HOOK": "tok-all"}.get,
		Now:       func() time.Time { return h.now },
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.m = m
	return h
}

type envMap map[string]string

func (m envMap) get(k string) string { return m[k] }

func (h *harness) put(rows ...row) {
	b, _ := json.Marshal(map[string]any{"messages": rows})
	if err := os.WriteFile(h.store, b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) read() {
	h.t.Helper()
	if err := h.m.Read(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) take() []hit {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.hits
	h.hits = nil
	return out
}

func TestOneReadPerTickDebouncedDeliveryEchoDroppedAndDeduped(t *testing.T) {
	h := newHarness(t)
	old := msg("ops@g.us", "m0", "2026-11-08T09:00:00Z", "history", false)
	h.put(old)
	h.read() // first read: cursor at the newest, NO history
	h.m.Flush(context.Background(), true)
	if got := h.take(); len(got) != 0 {
		t.Fatalf("history delivered: %+v", got)
	}

	if err := os.WriteFile(h.ledger, []byte(`{"id":"echo1","chat":"ops@g.us","at":"2026-11-08T09:59:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.put(old,
		msg("ops@g.us", "m1", "2026-11-08T09:01:00Z", "deploy broke", false),
		msg("ops@g.us", "echo1", "2026-11-08T09:01:01Z", "our own send", true),
		msg("ops@g.us", "m2", "2026-11-08T09:01:02Z", "owner on the phone", true),
		msg("dm@s.whatsapp.net", "d1", "2026-11-08T09:01:03Z", "hi", false),
	)
	calls0 := countLines(h.store + ".calls")
	h.read()
	if n := countLines(h.store+".calls") - calls0; n != 2 { // show anchor + ONE list, for every watch
		t.Fatalf("adapter made %d wacli calls for one tick, want 2 (one combined read)", n)
	}

	h.now = h.now.Add(time.Second)
	h.m.Flush(context.Background(), false)
	if got := h.take(); len(got) != 0 {
		t.Fatalf("delivered before the chat was quiet for 3s: %+v", got)
	}
	h.now = h.now.Add(3 * time.Second)
	h.m.Flush(context.Background(), false)
	got := h.take()
	byPath := map[string][]hit{}
	for _, x := range got {
		byPath[x.path] = append(byPath[x.path], x)
	}
	ops := byPath["/hooks/p/ops-bot"]
	if len(ops) != 1 || ops[0].auth != "Bearer tok-ops" || !strings.HasPrefix(ops[0].key, "wa-ops-ops@g.us-") {
		t.Fatalf("ops hook = %+v", ops)
	}
	batch := ops[0].body["messages"].([]any)
	if len(batch) != 2 || batch[0].(map[string]any)["id"] != "m1" || batch[1].(map[string]any)["fromOwner"] != true {
		t.Fatalf("ops batch = %v (echo must be dropped, owner kept)", batch)
	}
	if ops[0].body["message"].(map[string]any)["id"] != "m2" {
		t.Fatalf("message = newest of the batch, got %v", ops[0].body["message"])
	}
	if len(byPath["/hooks/p/archive"]) != 2 { // one per chat
		t.Fatalf("archive hook = %+v", byPath["/hooks/p/archive"])
	}

	// Nothing new: no delivery. Rewind the cursor: dedupe still holds.
	h.read()
	h.m.st.Cursor = "ops@g.us|m0"
	h.read()
	h.now = h.now.Add(time.Minute)
	h.m.Flush(context.Background(), true)
	if got := h.take(); len(got) != 0 {
		t.Fatalf("re-delivered: %+v", got)
	}

	// Restart from the state file: cursor and seen ids survive.
	m2, err := New(h.m.cfg)
	if err != nil || m2.st.Cursor == "" || !m2.seen["ops@g.us|m1"] {
		t.Fatalf("state not persisted: %+v %v", m2.st, err)
	}
}

func TestAFailedHookKeepsTheBatchForTheNextTick(t *testing.T) {
	h := newHarness(t)
	h.put(msg("ops@g.us", "m0", "2026-11-08T09:00:00Z", "a", false))
	h.read()
	h.put(msg("ops@g.us", "m0", "2026-11-08T09:00:00Z", "a", false), msg("ops@g.us", "m1", "2026-11-08T09:00:05Z", "b", false))
	h.read()
	h.failing = true
	h.m.Flush(context.Background(), true)
	h.failing = false
	h.m.Flush(context.Background(), true)
	got := h.take()
	if len(got) != 2 { // ops + all, delivered on the retry
		t.Fatalf("retry delivered %d, want 2: %+v", len(got), got)
	}
}

func TestOneMonitorPerAccount(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	rel, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(p); err == nil || !strings.Contains(err.Error(), "one per account") {
		t.Fatalf("second lock: %v", err)
	}
	rel()
	if rel2, err := Lock(p); err != nil {
		t.Fatal(err)
	} else {
		rel2()
	}
}

func countLines(p string) int {
	b, _ := os.ReadFile(p)
	return strings.Count(string(b), "\n")
}

// Watches come from wfnexus on every read: one registered while the monitor
// runs is used on the next tick, without a restart.
func TestAWatchRegisteredWhileRunningIsUsedOnTheNextRead(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	all := h.ws
	h.ws = nil
	h.mu.Unlock()
	h.put(msg("ops@g.us", "m0", "2026-11-08T09:00:00Z", "history", false))
	h.read()
	h.put(msg("ops@g.us", "m1", "2026-11-08T09:01:00Z", "nobody listening", false))
	h.read()
	h.m.Flush(context.Background(), true)
	if got := h.take(); len(got) != 0 {
		t.Fatalf("delivered with no watches: %+v", got)
	}
	h.mu.Lock()
	h.ws = all[:1] // `wfx module watch add … ops`
	h.mu.Unlock()
	h.put(msg("ops@g.us", "m1", "2026-11-08T09:01:00Z", "nobody listening", false),
		msg("ops@g.us", "m2", "2026-11-08T09:02:00Z", "now somebody is", false))
	h.read()
	h.m.Flush(context.Background(), true)
	got := h.take()
	if len(got) != 1 || got[0].path != "/hooks/p/ops-bot" {
		t.Fatalf("after registering: %+v", got)
	}
}

// The heartbeat comes from inside the loop, after each read.
func TestTheLoopHeartbeatsAfterEachRead(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	beats := 0
	h.m.cfg.Heartbeat = func(context.Context) error {
		mu.Lock()
		beats++
		mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		for {
			mu.Lock()
			n := beats
			mu.Unlock()
			if n > 0 || ctx.Err() != nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	_ = h.m.Run(ctx)
	mu.Lock()
	defer mu.Unlock()
	if beats == 0 {
		t.Fatal("no heartbeat from the loop")
	}
}
