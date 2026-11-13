package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Usage per project per UTC day (migration 000032).

// UsageDay is one project's usage on one UTC day.
type UsageDay struct {
	Project string  `json:"project"`
	Day     string  `json:"day"`
	Runs    int     `json:"runs"`
	Steps   int     `json:"steps"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"costUsd"`
}

// Day is the UTC day key a time falls on.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// AccountRun adds what a run has spent, since it was last accounted, to its
// project's row for `day`. The first call for a run also counts the run.
func (s *Store) AccountRun(ctx context.Context, runID uuid.UUID, project string, day string) error {
	steps, err := s.ListSteps(ctx, runID)
	if err != nil {
		return err
	}
	var nSteps int
	var tokens int64
	var cost float64
	for _, st := range steps {
		switch st.Status {
		case "pending", "skipped":
			continue
		}
		nSteps++
		if len(st.Usage) == 0 {
			continue
		}
		var u stepUsage
		if json.Unmarshal(st.Usage, &u) != nil {
			continue
		}
		tokens += u.PromptTokens + u.CompletionTokens
		if u.CostUSD != nil {
			cost += *u.CostUSD
		}
	}
	var prevSteps int
	var prevTokens int64
	var prevCost float64
	newRun := 0
	err = s.qrow(ctx, `SELECT steps, tokens, cost_usd FROM usage_runs WHERE run_id=$1`, runID.String()).
		Scan(&prevSteps, &prevTokens, &prevCost)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		newRun = 1
		if _, err := s.exec(ctx, `INSERT INTO usage_runs (run_id, project, steps, tokens, cost_usd) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (run_id) DO NOTHING`, runID.String(), project, nSteps, tokens, cost); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if _, err := s.exec(ctx, `UPDATE usage_runs SET steps=$2, tokens=$3, cost_usd=$4 WHERE run_id=$1`,
			runID.String(), nSteps, tokens, cost); err != nil {
			return err
		}
	}
	dSteps, dTokens, dCost := max(nSteps-prevSteps, 0), max(tokens-prevTokens, 0), max(cost-prevCost, 0)
	_, err = s.exec(ctx, `INSERT INTO project_usage (project, day, runs, steps, tokens, cost_usd) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (project, day) DO UPDATE SET runs = project_usage.runs + $3, steps = project_usage.steps + $4,
			tokens = project_usage.tokens + $5, cost_usd = project_usage.cost_usd + $6`,
		project, day, newRun, dSteps, dTokens, dCost)
	return err
}

// UsageDays lists a project's usage between two days, inclusive, by day.
func (s *Store) UsageDays(ctx context.Context, project, from, to string) ([]UsageDay, error) {
	rows, err := s.query(ctx, `SELECT project, day, runs, steps, tokens, cost_usd FROM project_usage
		WHERE project=$1 AND day >= $2 AND day <= $3 ORDER BY day`, project, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageDay{}
	for rows.Next() {
		var u UsageDay
		if err := rows.Scan(&u.Project, &u.Day, &u.Runs, &u.Steps, &u.Tokens, &u.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Quotas are a project's daily limits; 0 means none.
type Quotas struct {
	RunsPerDay    int     `json:"runsPerDay"`
	CostPerDayUSD float64 `json:"costPerDayUsd"`
}

// SetQuotas sets a project's daily limits.
func (s *Store) SetQuotas(ctx context.Context, project string, q Quotas) error {
	if err := s.RegisterProject(ctx, project); err != nil {
		return err
	}
	_, err := s.exec(ctx, `UPDATE projects SET max_runs_per_day=$2, max_cost_per_day=$3 WHERE name=$1`,
		project, q.RunsPerDay, q.CostPerDayUSD)
	return err
}

// GetQuotas reads a project's limits; a project with no row has none.
func (s *Store) GetQuotas(ctx context.Context, project string) (Quotas, error) {
	var q Quotas
	err := s.qrow(ctx, `SELECT max_runs_per_day, max_cost_per_day FROM projects WHERE name=$1`, project).
		Scan(&q.RunsPerDay, &q.CostPerDayUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return Quotas{}, nil
	}
	return q, err
}

// RunsCreatedSince counts a project's runs created at or after t — what the
// runs-per-day quota is checked against, so a run counts the moment it exists.
func (s *Store) RunsCreatedSince(ctx context.Context, project string, t time.Time) (int, error) {
	var n int
	err := s.qrow(ctx, `SELECT count(*) FROM workflow_runs WHERE project=$1 AND created_at >= $2`, project, s.timeArg(t)).Scan(&n)
	return n, err
}

// CostOn is a project's accounted spend on one day.
func (s *Store) CostOn(ctx context.Context, project, day string) (float64, error) {
	var c float64
	err := s.qrow(ctx, `SELECT cost_usd FROM project_usage WHERE project=$1 AND day=$2`, project, day).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return c, err
}
