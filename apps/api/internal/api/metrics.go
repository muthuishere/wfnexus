package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// metrics is the System → Metrics page's one request: everything aggregated
// server-side for a window of 24h, 7d or 30d (?window=, default 24h).
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	if _, ok := store.MetricsWindows[window]; !ok {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("window must be one of 24h, 7d, 30d; got %q", window))
		return
	}
	m, err := s.store.Metrics(r.Context(), window, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// prometheus is the same numbers in the text exposition format, for anyone who
// does run Prometheus. Gauges over the last 24h plus the live state: the
// server keeps no in-process counters, so a counter type here would lie
// across restarts.
func (s *Server) prometheus(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.Metrics(r.Context(), "24h", time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var b strings.Builder
	gauge := func(name, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	}
	gauge("wfnexus_runs_24h", "Runs created in the last 24h, by status.")
	statuses := make([]string, 0, len(m.Runs.ByStatus))
	for st := range m.Runs.ByStatus {
		statuses = append(statuses, st)
	}
	sort.Strings(statuses)
	for _, st := range statuses {
		fmt.Fprintf(&b, "wfnexus_runs_24h{status=%q} %d\n", st, m.Runs.ByStatus[st])
	}
	one := func(name, help string, v any) {
		gauge(name, help)
		fmt.Fprintf(&b, "%s %v\n", name, v)
	}
	one("wfnexus_step_failure_ratio_24h", "Failed steps over finished steps, last 24h.", m.Steps.FailureRate)
	one("wfnexus_step_retry_ratio_24h", "Steps that took more than one attempt, last 24h.", m.Steps.RetryRate)
	one("wfnexus_approvals_waiting", "Steps waiting on a person now.", m.Approvals.Waiting)
	one("wfnexus_runs_queued", "Runs queued now.", m.Queue.RunsQueued)
	one("wfnexus_runs_running", "Runs running now.", m.Queue.RunsRunning)
	one("wfnexus_worker_jobs_queued", "Worker jobs queued now.", m.Queue.JobsQueued)
	one("wfnexus_workers_online", "Workers seen in the last 90s.", m.Workers.Online)
	one("wfnexus_workers_total", "Workers registered.", m.Workers.Total)
	one("wfnexus_llm_calls_24h", "LLM calls, last 24h.", m.LLM.Calls)
	one("wfnexus_llm_errors_24h", "LLM calls that errored, last 24h.", m.LLM.Errors)
	one("wfnexus_provider_fallbacks_24h", "Provider fallbacks taken, last 24h.", m.LLM.Fallbacks)
	one("wfnexus_llm_cost_usd_24h", "Priced LLM spend in USD, last 24h.", m.LLM.CostUSD)
	gauge("wfnexus_llm_tokens_24h", "LLM tokens, last 24h, by provider, model and direction.")
	for _, mu := range m.LLM.Models {
		fmt.Fprintf(&b, "wfnexus_llm_tokens_24h{provider=%q,model=%q,direction=\"prompt\"} %d\n", mu.Provider, mu.Model, mu.PromptTokens)
		fmt.Fprintf(&b, "wfnexus_llm_tokens_24h{provider=%q,model=%q,direction=\"completion\"} %d\n", mu.Provider, mu.Model, mu.CompletionTokens)
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
