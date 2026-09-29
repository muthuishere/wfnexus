package eval

import (
	"context"
	"encoding/json"
	"time"
)

// Execution is what one run of the workflow left behind: its final status and,
// per step, the typed output and the usage aggregate (ADR 0020) the step row
// carries.
type Execution struct {
	Status  string
	Error   string
	Outputs map[string]json.RawMessage
	Usage   map[string]json.RawMessage
}

// Executor runs a workflow once on one provider. The runner does not know how
// — in this process on a throwaway store (Local), or anywhere else — so the
// matrix logic is tested without a model and without an engine.
type Executor interface {
	Execute(ctx context.Context, provider, workflow string, input map[string]any) (*Execution, error)
}

// Cell is one case on one provider.
type Cell struct {
	Provider string   `json:"provider"`
	Status   string   `json:"status"`
	Pass     bool     `json:"pass"`
	Error    string   `json:"error,omitempty"`
	Results  []Result `json:"results"`
	// Turns is LLM round trips summed over the steps.
	Turns            int     `json:"turns"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CostUsd          float64 `json:"costUsd"`
	// CostKnown is false when any step's price was unknown. Unknown is not
	// zero (ADR 0020): the table prints "?" rather than inventing $0.00.
	CostKnown bool  `json:"costKnown"`
	WallMs    int64 `json:"wallMs"`
}

// Row is one case across every provider, in the order they were named.
type Row struct {
	Case  string `json:"case"`
	Cells []Cell `json:"cells"`
}

// Total is one provider's column summed.
type Total struct {
	Provider         string  `json:"provider"`
	Passed           int     `json:"passed"`
	Cases            int     `json:"cases"`
	PassRate         float64 `json:"passRate"`
	Turns            int     `json:"turns"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CostUsd          float64 `json:"costUsd"`
	CostKnown        bool    `json:"costKnown"`
	WallMs           int64   `json:"wallMs"`
}

// Matrix is the portability matrix: rows are cases, columns are providers.
type Matrix struct {
	Workflow  string   `json:"workflow"`
	Providers []string `json:"providers"`
	Rows      []Row    `json:"rows"`
	Totals    []Total  `json:"totals"`
}

// Run executes every case on every provider, sequentially. Sequential is a
// choice, not a shortcut: wall time is a column, and two backends sharing one
// machine's CPU (or one rate-limited account) would each report the other's
// load. One run is one sample — variance is ADR 0019's open question, and a
// repeat count belongs with that decision, not guessed at here.
func Run(ctx context.Context, x Executor, c *Corpus, providers []string) *Matrix {
	m := &Matrix{Workflow: c.Workflow, Providers: providers}
	totals := make([]Total, len(providers))
	for i, p := range providers {
		totals[i] = Total{Provider: p, CostKnown: true}
	}
	for _, cs := range c.Cases {
		row := Row{Case: cs.Name}
		for i, p := range providers {
			cell := runCell(ctx, x, c.Workflow, p, cs)
			row.Cells = append(row.Cells, cell)
			t := &totals[i]
			t.Cases++
			if cell.Pass {
				t.Passed++
			}
			t.Turns += cell.Turns
			t.PromptTokens += cell.PromptTokens
			t.CompletionTokens += cell.CompletionTokens
			t.CostUsd += cell.CostUsd
			t.CostKnown = t.CostKnown && cell.CostKnown
			t.WallMs += cell.WallMs
		}
		m.Rows = append(m.Rows, row)
	}
	for i := range totals {
		if totals[i].Cases > 0 {
			totals[i].PassRate = float64(totals[i].Passed) / float64(totals[i].Cases)
		}
		totals[i].CostUsd = round6(totals[i].CostUsd)
	}
	m.Totals = totals
	return m
}

func runCell(ctx context.Context, x Executor, wf, provider string, cs Case) Cell {
	cell := Cell{Provider: provider, CostKnown: true}
	start := time.Now()
	ex, err := x.Execute(ctx, provider, wf, cs.Input)
	cell.WallMs = time.Since(start).Milliseconds()
	if err != nil {
		cell.Status, cell.Error, cell.CostKnown = "error", err.Error(), false
		return cell
	}
	cell.Status, cell.Error = ex.Status, ex.Error
	for _, raw := range ex.Usage {
		var u struct {
			LLMCalls         int      `json:"llmCalls"`
			PromptTokens     int      `json:"promptTokens"`
			CompletionTokens int      `json:"completionTokens"`
			CostUsd          *float64 `json:"costUsd"`
		}
		if len(raw) == 0 || json.Unmarshal(raw, &u) != nil {
			continue
		}
		cell.Turns += u.LLMCalls
		cell.PromptTokens += u.PromptTokens
		cell.CompletionTokens += u.CompletionTokens
		if u.CostUsd != nil {
			cell.CostUsd += *u.CostUsd
		} else if u.LLMCalls > 0 {
			// Tokens were spent at a price nobody knows.
			cell.CostKnown = false
		}
	}
	cell.CostUsd = round6(cell.CostUsd)
	pass := ex.Status == cs.ExpectedStatus()
	for _, a := range cs.Assert {
		r := Check(a, ex.Outputs)
		cell.Results = append(cell.Results, r)
		pass = pass && r.Pass
	}
	cell.Pass = pass
	return cell
}

func round6(f float64) float64 { return float64(int64(f*1e6+0.5)) / 1e6 }
