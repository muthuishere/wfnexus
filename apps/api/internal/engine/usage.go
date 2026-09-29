package engine

import (
	"sync"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// flushEvery is the floor between two `usage` writes for one step.
//
// The trigger is TIME, not a count of events. A count is wrong in both
// directions: a step that makes one 90-second call would write nothing for
// ninety seconds at "every 5 events", and a step hammering a local tool would
// write hundreds of times a second at the same setting. Coalescing on a
// deadline bounds the write rate to 1/s per running step whatever the step
// does, which is also the rate a human reading a progress column can perceive.
//
// No ticker, therefore no goroutine and no shutdown to get wrong: the flush
// rides on the metric callback, which is the only thing that can have changed
// the numbers. A step that goes quiet stops writing because nothing accrued.
const flushEvery = time.Second

// pricing is the per-million-token price a step is charged at, and whether we
// KNOW it. Known=false is not zero (ADR 0020). Source says where the number came
// from — `default` is an approximate seed price, the UI marks it so.
type pricing struct {
	InPerM  float64
	OutPerM float64
	Known   bool
	Source  string
}

// priceOf prices a call to model on provider p against table (catalog.PriceFor). A `cli` or
// `acp` provider is a program on this machine and bills no tokens: a KNOWN
// zero — the number a local-first runtime can show that a hosted product
// structurally cannot.
func priceOf(p catalog.Provider, model string, table map[string]catalog.Price) pricing {
	pr, src, ok := catalog.PriceFor(p, model, table)
	return pricing{InPerM: pr.In, OutPerM: pr.Out, Known: ok, Source: src}
}

// cost is the money for a token pair, and false when no price is known.
func (pr pricing) cost(prompt, completion int) (float64, bool) {
	if !pr.Known {
		return 0, false
	}
	return float64(prompt)/1e6*pr.InPerM + float64(completion)/1e6*pr.OutPerM, true
}

// modelUsage is one model's share of a step.
type modelUsage struct {
	Calls      int   `json:"calls"`
	Prompt     int   `json:"promptTokens"`
	Completion int   `json:"completionTokens"`
	Ms         int64 `json:"ms"`
}

// usageAccum folds the MetricEvents a step already emits into the aggregate the
// step row carries. The event log stays the append-only truth; this is the
// queryable roll-up over it (ADR 0020).
//
// It is mutex-guarded because the metric sink is called from the agent's own
// goroutines — a step with a sub-agent team has several in flight.
type usageAccum struct {
	mu    sync.Mutex
	start time.Time
	price pricing

	llmCalls   int
	llmErrors  int
	toolCalls  int
	toolErrors int
	prompt     int
	completion int
	llmMs      int64
	toolMs     int64
	models     map[string]*modelUsage

	dirty     bool
	lastFlush time.Time
	// flush persists a snapshot. Called OUTSIDE the lock.
	flush func(u map[string]any, turns int)
}

func newUsageAccum(pr pricing, flush func(map[string]any, int)) *usageAccum {
	now := time.Now()
	return &usageAccum{start: now, lastFlush: now, price: pr, models: map[string]*modelUsage{}, flush: flush}
}

// record folds one event in and writes the aggregate if the cadence allows.
func (u *usageAccum) record(m tn.MetricEvent) {
	u.mu.Lock()
	switch m.Event {
	case "llm":
		u.llmCalls++
		if m.Status != "ok" {
			u.llmErrors++
		}
		u.prompt += m.PromptTokens
		u.completion += m.CompletionTokens
		u.llmMs += m.Ms
		mu := u.models[m.Model]
		if mu == nil {
			mu = &modelUsage{}
			u.models[m.Model] = mu
		}
		mu.Calls++
		mu.Prompt += m.PromptTokens
		mu.Completion += m.CompletionTokens
		mu.Ms += m.Ms
	case "tool":
		u.toolCalls++
		if m.IsError {
			u.toolErrors++
		}
		u.toolMs += m.Ms
	default:
		u.mu.Unlock()
		return
	}
	u.dirty = true
	if time.Since(u.lastFlush) < flushEvery {
		u.mu.Unlock()
		return
	}
	u.lastFlush = time.Now()
	u.dirty = false
	snap, turns := u.snapshotLocked()
	u.mu.Unlock()
	u.flush(snap, turns)
}

// final writes the last aggregate unconditionally: the cadence means the last
// events of a step are usually still unwritten when it ends.
func (u *usageAccum) final() (map[string]any, int) {
	u.mu.Lock()
	u.dirty = false
	u.lastFlush = time.Now()
	snap, turns := u.snapshotLocked()
	u.mu.Unlock()
	return snap, turns
}

// snapshotLocked renders the aggregate. The caller holds the lock.
//
// `totalTokens` is kept at the top level with exactly its old meaning, so every
// reader written against the write-once blob keeps working: this EXTENDS the
// existing `usage` jsonb rather than adding columns. A column per counter would
// need a migration in two dialects for a shape that is still moving (per-model
// is a map, and cache-read/write tokens are the obvious next arrivals), and the
// column it would most want — cost — is derived, not measured.
func (u *usageAccum) snapshotLocked() (map[string]any, int) {
	total := u.prompt + u.completion
	out := map[string]any{
		"totalTokens":      total,
		"promptTokens":     u.prompt,
		"completionTokens": u.completion,
		"llmCalls":         u.llmCalls,
		"toolCalls":        u.toolCalls,
		"elapsedMs":        time.Since(u.start).Milliseconds(),
		"llmMs":            u.llmMs,
		"toolMs":           u.toolMs,
	}
	if u.llmErrors > 0 {
		out["llmErrors"] = u.llmErrors
	}
	if u.toolErrors > 0 {
		out["toolErrors"] = u.toolErrors
	}
	if len(u.models) > 0 {
		models := map[string]any{}
		for name, m := range u.models {
			models[name] = *m
		}
		out["models"] = models
	}
	if c, ok := u.price.cost(u.prompt, u.completion); ok {
		out["costUsd"] = round6(c)
		out["pricePerMIn"] = u.price.InPerM
		out["pricePerMOut"] = u.price.OutPerM
		out["priceSource"] = u.price.Source
	} else {
		// Tokens without a price. Said in the data, so a UI can render
		// "unknown" rather than inventing a zero.
		out["costUnknown"] = true
	}
	// Turns are LLM round trips; the step row's `turns` column is what the run
	// page reads, and it must move while the step runs, not at its end.
	return out, u.llmCalls
}

func round6(f float64) float64 {
	return float64(int64(f*1e6+0.5)) / 1e6
}
