package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetricsEndpoint(t *testing.T) {
	h, _ := testServer(t, "127.0.0.1:8090")
	w := do(h, "GET", "/api/metrics?window=7d", "", nil)
	if w.Code != 200 {
		t.Fatalf("metrics: %d %s", w.Code, w.Body)
	}
	var m struct {
		Window   string `json:"window"`
		Timeline []any  `json:"timeline"`
		Runs     struct {
			Total int `json:"total"`
		} `json:"runs"`
		Workers struct {
			Workers []any `json:"workers"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Window != "7d" || len(m.Timeline) != 8 || m.Workers.Workers == nil {
		t.Fatalf("shape: %s", w.Body)
	}
	if w := do(h, "GET", "/api/metrics?window=1y", "", nil); w.Code != 400 {
		t.Fatalf("an unknown window is refused, got %d", w.Code)
	}
	p := do(h, "GET", "/metrics", "", nil)
	if p.Code != 200 || !strings.Contains(p.Body.String(), "# TYPE wfnexus_runs_queued gauge") {
		t.Fatalf("prometheus: %d %s", p.Code, p.Body)
	}
}

// Off loopback both need a subject, like every other read.
func TestMetricsRequireASubjectOffLoopback(t *testing.T) {
	h, _ := testServer(t, "0.0.0.0:8090")
	for _, path := range []string{"/api/metrics", "/metrics"} {
		if w := do(h, "GET", path, "", nil); w.Code != 401 {
			t.Fatalf("%s without a token: %d", path, w.Code)
		}
	}
}
