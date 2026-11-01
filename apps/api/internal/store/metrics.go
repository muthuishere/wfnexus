package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

// Metrics is the server's own operational picture over a time window: how runs
// went, how long steps took, what people kept waiting, what the models cost.
// It is read straight from the tables the engine already writes — runs, steps,
// their `usage` roll-up (ADR 0020) and the event log — so a local install has
// a metrics page with no Prometheus, no Grafana and no extra writes.
//
// Counting and grouping happen in SQL. Percentiles and the per-model split of
// the `usage` JSON happen here, once, over the window's rows: SQLite has no
// percentile function and the two engines spell JSON paths differently, and a
// window of steps is small enough that one pass in Go is cheaper than two
// dialects of the same aggregate drifting apart.
type Metrics struct {
	Window      string    `json:"window"`
	Since       time.Time `json:"since"`
	GeneratedAt time.Time `json:"generatedAt"`
	// BucketSeconds is the width of one Timeline bucket (an hour for 24h, a
	// day otherwise).
	BucketSeconds int64 `json:"bucketSeconds"`

	Runs      RunMetrics      `json:"runs"`
	Timeline  []TimeBucket    `json:"timeline"`
	Steps     StepMetrics     `json:"steps"`
	Approvals ApprovalMetrics `json:"approvals"`
	LLM       LLMMetrics      `json:"llm"`
	Workers   WorkerMetrics   `json:"workers"`
	Queue     QueueMetrics    `json:"queue"`
}

type RunMetrics struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"byStatus"`
	// ByWorkflow is newest-busiest first: the workflows with the most runs.
	ByWorkflow []WorkflowRuns `json:"byWorkflow"`
}

type WorkflowRuns struct {
	Project   string         `json:"project"`
	Workflow  string         `json:"workflow"`
	Total     int            `json:"total"`
	ByStatus  map[string]int `json:"byStatus"`
	P50Ms     int64          `json:"p50Ms"`
	P95Ms     int64          `json:"p95Ms"`
	Durations int            `json:"durations"` // how many finished runs the percentiles cover
}

type TimeBucket struct {
	Start    time.Time      `json:"start"`
	ByStatus map[string]int `json:"byStatus"`
}

type StepMetrics struct {
	Total       int     `json:"total"`
	Finished    int     `json:"finished"`
	Failed      int     `json:"failed"`
	Retried     int     `json:"retried"`
	FailureRate float64 `json:"failureRate"` // failed / finished
	RetryRate   float64 `json:"retryRate"`   // ran more than one attempt / finished
	ToolCalls   int     `json:"toolCalls"`
	ToolErrors  int     `json:"toolErrors"`
	// Slowest is the steps with the highest p95, at most 15.
	Slowest []StepDuration `json:"slowest"`
}

type StepDuration struct {
	Project  string `json:"project"`
	Workflow string `json:"workflow"`
	Step     string `json:"step"`
	Count    int    `json:"count"`
	P50Ms    int64  `json:"p50Ms"`
	P95Ms    int64  `json:"p95Ms"`
}

type ApprovalMetrics struct {
	// Waiting is NOW, whatever the window: a pause from last month that is
	// still open is exactly the one that matters.
	Waiting       int            `json:"waiting"`
	OldestWaiting *time.Time     `json:"oldestWaiting"`
	Resolved      int            `json:"resolved"`
	ByResolution  map[string]int `json:"byResolution"`
	P50Ms         int64          `json:"p50Ms"` // time to resolve
	P95Ms         int64          `json:"p95Ms"`
}

type LLMMetrics struct {
	Calls            int     `json:"calls"`
	Errors           int     `json:"errors"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CostUSD          float64 `json:"costUsd"`
	// UnpricedSteps used tokens with no known price: the cost above is a
	// floor, never a guess (ADR 0020).
	UnpricedSteps int `json:"unpricedSteps"`
	// Fallbacks counts a provider that could not be reached and was replaced
	// by the next in its chain.
	Fallbacks int          `json:"fallbacks"`
	Models    []ModelUsage `json:"models"`
}

type ModelUsage struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Calls            int     `json:"calls"`
	Errors           int     `json:"errors"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CostUSD          float64 `json:"costUsd"`
	CostKnown        bool    `json:"costKnown"`
}

type WorkerMetrics struct {
	Total   int            `json:"total"`
	Online  int            `json:"online"`
	Workers []WorkerHealth `json:"workers"`
}

type WorkerHealth struct {
	Name     string    `json:"name"`
	Labels   []string  `json:"labels"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"lastSeen"`
	Version  string    `json:"version"`
}

type QueueMetrics struct {
	RunsQueued  int `json:"runsQueued"`
	RunsRunning int `json:"runsRunning"`
	JobsQueued  int `json:"jobsQueued"`
	JobsLeased  int `json:"jobsLeased"`
	// OldestQueued is the age of the longest-waiting queued run or job.
	OldestQueued *time.Time `json:"oldestQueued"`
}

// MetricsWindows are the ranges the page offers. Anything else is refused by
// the handler rather than silently widened.
var MetricsWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// timeArg binds a cutoff. SQLite keeps timestamps as text in the
// `YYYY-MM-DD HH:MM:SS.fff` shape its defaults write, which compares
// lexically; a time.Time would be bound in a different spelling.
func (s *Store) timeArg(t time.Time) any {
	if s.d.isSQLite() {
		return t.UTC().Format("2006-01-02 15:04:05.000")
	}
	return t
}

// Metrics aggregates everything since `now - window`.
func (s *Store) Metrics(ctx context.Context, window string, now time.Time) (*Metrics, error) {
	d, ok := MetricsWindows[window]
	if !ok {
		d, window = MetricsWindows["24h"], "24h"
	}
	since := now.Add(-d)
	bucket := time.Hour
	if d > 24*time.Hour {
		bucket = 24 * time.Hour
	}
	m := &Metrics{Window: window, Since: since, GeneratedAt: now, BucketSeconds: int64(bucket / time.Second)}
	if err := s.metricsRuns(ctx, m, since, bucket); err != nil {
		return nil, err
	}
	if err := s.metricsSteps(ctx, m, since); err != nil {
		return nil, err
	}
	if err := s.metricsLive(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) metricsRuns(ctx context.Context, m *Metrics, since time.Time, bucket time.Duration) error {
	// One row per run in the window, five narrow columns, on the created_at
	// index. Status counts by workflow and by time bucket both come from it.
	rows, err := s.query(ctx, `SELECT project, workflow, status, created_at, started_at, updated_at
		FROM workflow_runs WHERE created_at >= $1`, s.timeArg(since))
	if err != nil {
		return err
	}
	defer rows.Close()
	m.Runs.ByStatus = map[string]int{}
	type key struct{ p, w string }
	per := map[key]*WorkflowRuns{}
	durs := map[key][]int64{}
	buckets := map[int64]map[string]int{}
	for rows.Next() {
		var p, w, st string
		var created, updated time.Time
		var started *time.Time
		if err := rows.Scan(&p, &w, &st, &created, &started, &updated); err != nil {
			return err
		}
		m.Runs.Total++
		m.Runs.ByStatus[st]++
		k := key{p, w}
		wr := per[k]
		if wr == nil {
			wr = &WorkflowRuns{Project: p, Workflow: w, ByStatus: map[string]int{}}
			per[k] = wr
		}
		wr.Total++
		wr.ByStatus[st]++
		if started != nil && terminal(st) && !updated.Before(*started) {
			durs[k] = append(durs[k], updated.Sub(*started).Milliseconds())
		}
		b := created.Sub(since) / bucket
		bm := buckets[int64(b)]
		if bm == nil {
			bm = map[string]int{}
			buckets[int64(b)] = bm
		}
		bm[st]++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for k, wr := range per {
		ds := durs[k]
		wr.Durations = len(ds)
		wr.P50Ms, wr.P95Ms = pct(ds, 50), pct(ds, 95)
		m.Runs.ByWorkflow = append(m.Runs.ByWorkflow, *wr)
	}
	sort.Slice(m.Runs.ByWorkflow, func(i, j int) bool {
		a, b := m.Runs.ByWorkflow[i], m.Runs.ByWorkflow[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Project+"/"+a.Workflow < b.Project+"/"+b.Workflow
	})
	// A dense timeline: empty buckets are zero, not missing, so a chart's
	// x-axis is time and not "the hours something happened".
	n := int64(m.GeneratedAt.Sub(since) / bucket)
	for i := int64(0); i <= n; i++ {
		bm := buckets[i]
		if bm == nil {
			bm = map[string]int{}
		}
		m.Timeline = append(m.Timeline, TimeBucket{Start: since.Add(time.Duration(i) * bucket), ByStatus: bm})
	}
	return nil
}

func terminal(status string) bool {
	return status == "done" || status == "failed" || status == "cancelled"
}

// stepUsage is the slice of the `usage` roll-up the metrics read.
type stepUsage struct {
	Provider         string   `json:"provider"`
	PromptTokens     int64    `json:"promptTokens"`
	CompletionTokens int64    `json:"completionTokens"`
	LLMCalls         int      `json:"llmCalls"`
	LLMErrors        int      `json:"llmErrors"`
	ToolCalls        int      `json:"toolCalls"`
	ToolErrors       int      `json:"toolErrors"`
	CostUSD          *float64 `json:"costUsd"`
	CostUnknown      bool     `json:"costUnknown"`
	PriceIn          float64  `json:"pricePerMIn"`
	PriceOut         float64  `json:"pricePerMOut"`
	Models           map[string]struct {
		Calls      int   `json:"calls"`
		Prompt     int64 `json:"promptTokens"`
		Completion int64 `json:"completionTokens"`
	} `json:"models"`
}

func (s *Store) metricsSteps(ctx context.Context, m *Metrics, since time.Time) error {
	rows, err := s.query(ctx, `SELECT r.project, r.workflow, s.step_id, s.status, s.attempts,
			s.started_at, s.finished_at, s.usage, s.resolution, s.resolved_at
		FROM step_runs s JOIN workflow_runs r ON r.id = s.run_id
		WHERE r.created_at >= $1`, s.timeArg(since))
	if err != nil {
		return err
	}
	defer rows.Close()
	type key struct{ p, w, s string }
	durs := map[key][]int64{}
	var resolveMs []int64
	m.Approvals.ByResolution = map[string]int{}
	type mk struct{ p, m string }
	models := map[mk]*ModelUsage{}
	for rows.Next() {
		var p, w, id, st, resolution string
		var attempts int
		var started, finished, resolved *time.Time
		var raw json.RawMessage
		if err := rows.Scan(&p, &w, &id, &st, &attempts, &started, &finished, rawJSON{&raw}, &resolution, &resolved); err != nil {
			return err
		}
		m.Steps.Total++
		if st == "done" || st == "failed" || st == "approved" || st == "rejected" {
			m.Steps.Finished++
			if attempts > 1 {
				m.Steps.Retried++
			}
		}
		if st == "failed" {
			m.Steps.Failed++
		}
		if started != nil && finished != nil && !finished.Before(*started) && (st == "done" || st == "failed") {
			k := key{p, w, id}
			durs[k] = append(durs[k], finished.Sub(*started).Milliseconds())
		}
		if resolution != "" && resolved != nil {
			m.Approvals.Resolved++
			m.Approvals.ByResolution[resolution]++
			if started != nil && !resolved.Before(*started) {
				resolveMs = append(resolveMs, resolved.Sub(*started).Milliseconds())
			}
		}
		if len(raw) == 0 {
			continue
		}
		var u stepUsage
		if json.Unmarshal(raw, &u) != nil {
			continue
		}
		m.Steps.ToolCalls += u.ToolCalls
		m.Steps.ToolErrors += u.ToolErrors
		m.LLM.Calls += u.LLMCalls
		m.LLM.Errors += u.LLMErrors
		m.LLM.PromptTokens += u.PromptTokens
		m.LLM.CompletionTokens += u.CompletionTokens
		if u.CostUSD != nil {
			m.LLM.CostUSD += *u.CostUSD
		} else if u.CostUnknown && u.PromptTokens+u.CompletionTokens > 0 {
			m.LLM.UnpricedSteps++
		}
		prov := u.Provider
		if prov == "" {
			prov = "unknown"
		}
		known := u.CostUSD != nil
		add := func(model string, calls, errs int, in, out int64) {
			k := mk{prov, model}
			mu := models[k]
			if mu == nil {
				mu = &ModelUsage{Provider: prov, Model: model, CostKnown: true}
				models[k] = mu
			}
			mu.Calls += calls
			mu.Errors += errs
			mu.PromptTokens += in
			mu.CompletionTokens += out
			if known {
				// The step was priced at ONE rate (its provider's), so each
				// model's share is its own tokens at that rate.
				mu.CostUSD += float64(in)/1e6*u.PriceIn + float64(out)/1e6*u.PriceOut
			} else if in+out > 0 {
				mu.CostKnown = false
			}
		}
		if len(u.Models) == 0 {
			if u.LLMCalls > 0 || u.PromptTokens+u.CompletionTokens > 0 {
				add("unknown", u.LLMCalls, u.LLMErrors, u.PromptTokens, u.CompletionTokens)
			}
			continue
		}
		for name, mm := range u.Models {
			if name == "" {
				name = "unknown"
			}
			add(name, mm.Calls, 0, mm.Prompt, mm.Completion)
		}
		// Errors are counted per step, not per model; book them on the
		// provider's busiest model so the provider total is right.
		if u.LLMErrors > 0 {
			best, bestCalls := "", -1
			for name, mm := range u.Models {
				if mm.Calls > bestCalls || (mm.Calls == bestCalls && name < best) {
					best, bestCalls = name, mm.Calls
				}
			}
			if best == "" {
				best = "unknown"
			}
			models[mk{prov, best}].Errors += u.LLMErrors
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	m.LLM.CostUSD = round6(m.LLM.CostUSD)
	if m.Steps.Finished > 0 {
		m.Steps.FailureRate = float64(m.Steps.Failed) / float64(m.Steps.Finished)
		m.Steps.RetryRate = float64(m.Steps.Retried) / float64(m.Steps.Finished)
	}
	m.Approvals.P50Ms, m.Approvals.P95Ms = pct(resolveMs, 50), pct(resolveMs, 95)
	for k, ds := range durs {
		m.Steps.Slowest = append(m.Steps.Slowest, StepDuration{Project: k.p, Workflow: k.w, Step: k.s,
			Count: len(ds), P50Ms: pct(ds, 50), P95Ms: pct(ds, 95)})
	}
	sort.Slice(m.Steps.Slowest, func(i, j int) bool {
		a, b := m.Steps.Slowest[i], m.Steps.Slowest[j]
		if a.P95Ms != b.P95Ms {
			return a.P95Ms > b.P95Ms
		}
		return a.Workflow+a.Step < b.Workflow+b.Step
	})
	if len(m.Steps.Slowest) > 15 {
		m.Steps.Slowest = m.Steps.Slowest[:15]
	}
	for _, mu := range models {
		mu.CostUSD = round6(mu.CostUSD)
		m.LLM.Models = append(m.LLM.Models, *mu)
	}
	sort.Slice(m.LLM.Models, func(i, j int) bool {
		a, b := m.LLM.Models[i], m.LLM.Models[j]
		if ta, tb := a.PromptTokens+a.CompletionTokens, b.PromptTokens+b.CompletionTokens; ta != tb {
			return ta > tb
		}
		return a.Provider+a.Model < b.Provider+b.Model
	})

	// A fallback is written as a log line by the step that took it
	// (engine/step.go); counted in SQL on the event table's kind.
	return s.qrow(ctx, `SELECT count(*) FROM run_events
		WHERE kind = 'log' AND created_at >= $1 AND payload::text LIKE '%falling back to%'`,
		s.timeArg(since)).Scan(&m.LLM.Fallbacks)
}

// metricsLive is the part that is a state, not a window: what is queued,
// waiting and connected right now.
func (s *Store) metricsLive(ctx context.Context, m *Metrics) error {
	if err := s.qrow(ctx, `SELECT
			(SELECT count(*) FROM workflow_runs WHERE status = 'queued'),
			(SELECT count(*) FROM workflow_runs WHERE status = 'running'),
			(SELECT count(*) FROM worker_jobs WHERE status = 'queued'),
			(SELECT count(*) FROM worker_jobs WHERE status = 'leased'),
			(SELECT count(*) FROM step_runs WHERE status IN ('awaiting_approval','needs_input'))`).
		Scan(&m.Queue.RunsQueued, &m.Queue.RunsRunning, &m.Queue.JobsQueued, &m.Queue.JobsLeased, &m.Approvals.Waiting); err != nil {
		return err
	}
	oldest := func(q string) (*time.Time, error) {
		var t *time.Time
		err := s.qrow(ctx, q).Scan(&t)
		return t, err
	}
	var err error
	// min() over a timestamp column loses its type on SQLite (it comes back
	// as text), so the oldest is read with ORDER BY … LIMIT 1 instead.
	if m.Approvals.OldestWaiting, err = oldest(`SELECT started_at FROM step_runs
		WHERE status IN ('awaiting_approval','needs_input') AND started_at IS NOT NULL
		ORDER BY started_at LIMIT 1`); err != nil && !isNoRows(err) {
		return err
	}
	runQ, err := oldest(`SELECT created_at FROM workflow_runs WHERE status = 'queued' ORDER BY created_at LIMIT 1`)
	if err != nil && !isNoRows(err) {
		return err
	}
	jobQ, err := oldest(`SELECT created_at FROM worker_jobs WHERE status = 'queued' ORDER BY created_at LIMIT 1`)
	if err != nil && !isNoRows(err) {
		return err
	}
	m.Queue.OldestQueued = runQ
	if jobQ != nil && (runQ == nil || jobQ.Before(*runQ)) {
		m.Queue.OldestQueued = jobQ
	}

	workers, err := s.ListWorkers(ctx)
	if err != nil {
		return err
	}
	m.Workers.Workers = []WorkerHealth{}
	for _, w := range workers {
		m.Workers.Total++
		if w.Online() {
			m.Workers.Online++
		}
		m.Workers.Workers = append(m.Workers.Workers, WorkerHealth{Name: w.Name, Labels: w.Labels,
			Status: w.Status(), LastSeen: w.LastSeen, Version: w.Version})
	}
	return nil
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// pct is the nearest-rank percentile of xs (which it sorts), 0 when empty.
func pct(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	i := (p*len(xs) + 99) / 100 // ceil(p/100 * n)
	if i < 1 {
		i = 1
	}
	return xs[i-1]
}

func round6(f float64) float64 { return float64(int64(f*1e6+0.5)) / 1e6 }
