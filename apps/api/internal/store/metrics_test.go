package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

// The metrics are read from what the engine already wrote. This seeds that
// shape directly — runs, steps with a `usage` roll-up, a fallback log line,
// a waiting approval — and checks every section adds up, on both engines.
func TestMetricsAggregatesRunsStepsAndUsage(t *testing.T) {
	eachStore(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		wf := "metrics-" + uuid.NewString()[:8]
		model := "test-model-" + wf
		var ids []uuid.UUID
		t.Cleanup(func() {
			for _, id := range ids {
				_, _ = st.exec(context.Background(), `DELETE FROM workflow_runs WHERE id=$1`, id)
			}
		})
		base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

		// Run 1: done, one step that took 10s and was retried once, priced.
		// Run 2: failed, one step that took 30s and failed, unpriced, with a fallback.
		// Run 3: awaiting approval right now.
		type seed struct {
			status, step, stepStatus string
			attempts                 int
			secs                     int
			usage                    map[string]any
		}
		seeds := []seed{
			{"done", "build", "done", 2, 10, map[string]any{
				"provider": "openrouter", "promptTokens": 1000, "completionTokens": 500, "llmCalls": 3,
				"toolCalls": 4, "toolErrors": 1, "costUsd": 0.0025, "pricePerMIn": 1.0, "pricePerMOut": 3.0,
				"models": map[string]any{model: map[string]any{"calls": 3, "promptTokens": 1000, "completionTokens": 500}},
			}},
			{"failed", "build", "failed", 1, 30, map[string]any{
				"provider": "local", "promptTokens": 200, "completionTokens": 0, "llmCalls": 2, "llmErrors": 2,
				"costUnknown": true,
				"models": map[string]any{model: map[string]any{"calls": 2, "promptTokens": 200, "completionTokens": 0}},
			}},
			{"awaiting_approval", "ship", "awaiting_approval", 1, 0, nil},
		}
		for _, s := range seeds {
			r, err := st.CreateRun(ctx, "metrics-proj", wf, json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, r.ID)
			if err := st.UpdateRun(ctx, r.ID, "running", s.step, ""); err != nil {
				t.Fatal(err)
			}
			if err := st.UpdateRun(ctx, r.ID, s.status, s.step, ""); err != nil {
				t.Fatal(err)
			}
			if err := st.EnsureStep(ctx, r.ID, s.step, 0); err != nil {
				t.Fatal(err)
			}
			p := StepPatch{Status: ptr(s.stepStatus), Attempts: ptr(s.attempts), StartedAt: ptr(base)}
			if s.secs > 0 {
				p.FinishedAt = ptr(base.Add(time.Duration(s.secs) * time.Second))
			}
			if s.usage != nil {
				raw, _ := json.Marshal(s.usage)
				p.Usage = raw
			}
			if err := st.PatchStep(ctx, r.ID, s.step, p); err != nil {
				t.Fatal(err)
			}
			if s.status == "failed" {
				if _, err := st.AppendEvent(ctx, r.ID, s.step, "log", map[string]any{
					"text": "provider local could not be reached (boom) — falling back to openrouter"}); err != nil {
					t.Fatal(err)
				}
			}
		}

		m, err := st.Metrics(ctx, "24h", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Timeline) != 25 {
			t.Fatalf("24h at hourly buckets is 25 points (both ends), got %d", len(m.Timeline))
		}
		var mine *WorkflowRuns
		for i := range m.Runs.ByWorkflow {
			if m.Runs.ByWorkflow[i].Workflow == wf {
				mine = &m.Runs.ByWorkflow[i]
			}
		}
		if mine == nil {
			t.Fatalf("workflow %s missing from %+v", wf, m.Runs.ByWorkflow)
		}
		if mine.Total != 3 || mine.ByStatus["done"] != 1 || mine.ByStatus["failed"] != 1 || mine.ByStatus["awaiting_approval"] != 1 {
			t.Fatalf("by status: %+v", mine)
		}
		if mine.Durations != 2 {
			t.Fatalf("two terminal runs have a duration, got %d", mine.Durations)
		}

		var step *StepDuration
		for i := range m.Steps.Slowest {
			if m.Steps.Slowest[i].Workflow == wf && m.Steps.Slowest[i].Step == "build" {
				step = &m.Steps.Slowest[i]
			}
		}
		if step == nil || step.Count != 2 || step.P50Ms != 10_000 || step.P95Ms != 30_000 {
			t.Fatalf("build step p50/p95: %+v", step)
		}
		if m.Steps.Failed < 1 || m.Steps.Retried < 1 || m.Steps.FailureRate <= 0 || m.Steps.RetryRate <= 0 {
			t.Fatalf("failure/retry: %+v", m.Steps)
		}
		if m.LLM.Fallbacks < 1 {
			t.Fatalf("the fallback log line was not counted: %+v", m.LLM)
		}
		if m.LLM.UnpricedSteps < 1 || m.LLM.Errors < 2 {
			t.Fatalf("llm totals: %+v", m.LLM)
		}
		got := map[string]ModelUsage{}
		for _, mu := range m.LLM.Models {
			if mu.Model == model {
				got[mu.Provider] = mu
			}
		}
		or, loc := got["openrouter"], got["local"]
		if or.Calls != 3 || or.PromptTokens != 1000 || or.CompletionTokens != 500 || !or.CostKnown || or.CostUSD != 0.0025 {
			t.Fatalf("openrouter row: %+v", or)
		}
		if loc.Calls != 2 || loc.Errors != 2 || loc.CostKnown {
			t.Fatalf("local row (unpriced, two errors): %+v", loc)
		}
		if m.Approvals.Waiting < 1 || m.Approvals.OldestWaiting == nil {
			t.Fatalf("approvals waiting: %+v", m.Approvals)
		}

		// A window that starts after these runs were created sees none of them.
		later, err := st.Metrics(ctx, "24h", time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range later.Runs.ByWorkflow {
			if w.Workflow == wf {
				t.Fatalf("a run older than the window was counted: %+v", w)
			}
		}
		week, err := st.Metrics(ctx, "7d", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if week.BucketSeconds != 86400 || len(week.Timeline) != 8 {
			t.Fatalf("7d is daily buckets: %d x %d", week.BucketSeconds, len(week.Timeline))
		}
	})
}

func TestPercentile(t *testing.T) {
	if pct(nil, 95) != 0 {
		t.Fatal("empty is 0")
	}
	xs := []int64{5, 1, 4, 2, 3, 6, 7, 8, 9, 10}
	if pct(xs, 50) != 5 || pct(xs, 95) != 10 || pct(xs, 10) != 1 {
		t.Fatalf("nearest rank: p50=%d p95=%d p10=%d", pct(xs, 50), pct(xs, 95), pct(xs, 10))
	}
}
