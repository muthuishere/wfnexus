package api

// Usage, quotas and the per-token rate limit (plan 18, W4).
//
//	GET /api/projects/{name}/usage?from=YYYY-MM-DD&to=YYYY-MM-DD
//	    runs, steps, tokens and cost per UTC day (default: the last 30 days)
//	GET /api/projects/{name}/quotas
//	PUT /api/projects/{name}/quotas {runsPerDay, costPerDayUsd}   operator only
//
// A run over a quota, and a service token over its rate, are refused with 429,
// a Retry-After, and a body naming which limit — never a bare error.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// The service-token rate limit: a token bucket per token. Vars so tests can
// shorten them.
var (
	serviceRatePerMin = 600
	serviceRateBurst  = 120
)

// onRunStatus is the engine's one listener: signed events out, and usage.
func (s *Server) onRunStatus(ctx context.Context, runID uuid.UUID, status, step, errMsg string) {
	s.recordRunEvent(ctx, runID, status, step, errMsg)
	switch status {
	case "done", "failed", "cancelled":
		run, err := s.store.GetRun(ctx, runID)
		if err != nil {
			return
		}
		if err := s.store.AccountRun(ctx, runID, run.Project, store.Day(time.Now())); err != nil {
			log.Printf("usage: accounting run %s: %v", runID, err)
		}
	}
}

// admitRun is the engine's admission check: the project's daily quotas.
func (s *Server) admitRun(ctx context.Context, project string) error {
	q, err := s.store.GetQuotas(ctx, project)
	if err != nil || (q.RunsPerDay == 0 && q.CostPerDayUSD == 0) {
		return err
	}
	now := time.Now().UTC()
	if q.RunsPerDay > 0 {
		n, err := s.store.RunsCreatedSince(ctx, project, now.Truncate(24*time.Hour))
		if err != nil {
			return err
		}
		if n >= q.RunsPerDay {
			return &engine.QuotaError{Project: project, Quota: "runs_per_day", Limit: float64(q.RunsPerDay), Used: float64(n)}
		}
	}
	if q.CostPerDayUSD > 0 {
		c, err := s.store.CostOn(ctx, project, store.Day(now))
		if err != nil {
			return err
		}
		if c >= q.CostPerDayUSD {
			return &engine.QuotaError{Project: project, Quota: "cost_per_day", Limit: q.CostPerDayUSD, Used: c}
		}
	}
	return nil
}

// refuseQuota answers a quota refusal and reports whether err was one.
func refuseQuota(w http.ResponseWriter, err error) bool {
	var q *engine.QuotaError
	if !errors.As(err, &q) {
		return false
	}
	now := time.Now().UTC()
	retry := int(math.Ceil(now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now).Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": q.Error(), "code": "quota_" + q.Quota, "project": q.Project, "limit": q.Limit, "used": q.Used,
	})
	return true
}

// refuseRate answers a service token over its rate limit.
func refuseRate(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": fmt.Sprintf("rate limited: this token may make %d requests a minute (bursts of %d); retry after a second",
			serviceRatePerMin, serviceRateBurst),
		"code": "rate_limited",
	})
}

func (s *Server) projectUsage(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	now := time.Now().UTC()
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if to == "" {
		to = store.Day(now)
	}
	if from == "" {
		from = store.Day(now.AddDate(0, 0, -29))
	}
	for _, d := range []string{from, to} {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			writeErr(w, 400, fmt.Errorf("from and to are days, YYYY-MM-DD: %q", d))
			return
		}
	}
	days, err := s.store.UsageDays(r.Context(), name, from, to)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"project": name, "from": from, "to": to, "days": days})
}

func (s *Server) getQuotas(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	q, err := s.store.GetQuotas(r.Context(), name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, q)
}

// setQuotas is an operator's call (projects:write, no scoped grant reaches
// it): a project never raises its own limits.
func (s *Server) setQuotas(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	var q store.Quotas
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeErr(w, 400, err)
		return
	}
	if q.RunsPerDay < 0 || q.CostPerDayUSD < 0 {
		writeErr(w, 400, errors.New("quotas are 0 (no limit) or positive"))
		return
	}
	if err := s.store.SetQuotas(r.Context(), name, q); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, q)
}
