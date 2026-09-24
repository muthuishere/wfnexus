package engine

import (
	"testing"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

func f64(v float64) *float64 { return &v }

// TestUsageAccumCostMatchesTheHandComputedRun is the arithmetic check against
// the figure in ADR 0020: the 2026-09-24 `code-review` run spent 1,424,154
// prompt and 6,541 completion tokens on sonnet at $3/$15 per million, which was
// computed BY HAND as $4.37.
func TestUsageAccumCostMatchesTheHandComputedRun(t *testing.T) {
	u := newUsageAccum(pricing{InPerM: 3, OutPerM: 15, Known: true}, func(map[string]any, int) {})
	u.record(tn.MetricEvent{Event: "llm", Status: "ok", Model: "anthropic/claude-sonnet-4.5",
		PromptTokens: 1424154, CompletionTokens: 6541, Ms: 1000})
	snap, turns := u.final()

	if got := snap["costUsd"].(float64); got < 4.365 || got > 4.375 {
		t.Fatalf("costUsd = %v, want ~4.37", got)
	}
	if snap["totalTokens"].(int) != 1424154+6541 {
		t.Fatalf("totalTokens = %v", snap["totalTokens"])
	}
	if turns != 1 {
		t.Fatalf("turns = %d, want 1", turns)
	}
	if _, unknown := snap["costUnknown"]; unknown {
		t.Fatal("a priced provider must not report costUnknown")
	}
}

// A cli provider bills no tokens, so $0.00 is KNOWN, not missing — and an http
// provider with no price is unknown, not free. ADR 0020 turns on that
// distinction.
func TestPriceOfFreeVersusUnknown(t *testing.T) {
	if pr := priceOf(catalog.Provider{Kind: catalog.KindCLI}); !pr.Known {
		t.Fatal("a cli provider must report a KNOWN $0.00")
	}
	if pr := priceOf(catalog.Provider{Kind: catalog.KindACP}); !pr.Known {
		t.Fatal("an acp provider must report a KNOWN $0.00")
	}
	if pr := priceOf(catalog.Provider{Kind: catalog.KindHTTP}); pr.Known {
		t.Fatal("an http provider with no price must be UNKNOWN, not free")
	}
	pr := priceOf(catalog.Provider{Kind: catalog.KindHTTP, PricePerMIn: f64(3), PricePerMOut: f64(15)})
	if c, ok := pr.cost(1_000_000, 0); !ok || c != 3 {
		t.Fatalf("cost = %v ok=%v, want 3", c, ok)
	}

	free := newUsageAccum(priceOf(catalog.Provider{Kind: catalog.KindCLI}), func(map[string]any, int) {})
	free.record(tn.MetricEvent{Event: "llm", Status: "ok", Model: "claude-cli", PromptTokens: 99999})
	snap, _ := free.final()
	if snap["costUsd"].(float64) != 0 {
		t.Fatalf("a cli step must cost exactly 0, got %v", snap["costUsd"])
	}

	unknown := newUsageAccum(priceOf(catalog.Provider{Kind: catalog.KindHTTP}), func(map[string]any, int) {})
	unknown.record(tn.MetricEvent{Event: "llm", Status: "ok", Model: "x", PromptTokens: 10})
	snap, _ = unknown.final()
	if _, ok := snap["costUsd"]; ok {
		t.Fatal("an unpriced provider must report NO cost, never 0.00")
	}
	if snap["costUnknown"] != true {
		t.Fatal("an unpriced provider must say the cost is unknown")
	}
}

// The cadence is what makes a running step visible without making it chatty:
// the first event writes, the burst behind it does not, and `final` always does.
func TestUsageAccumFlushCadence(t *testing.T) {
	var writes int
	var last map[string]any
	u := newUsageAccum(pricing{Known: true}, func(m map[string]any, _ int) { writes++; last = m })
	u.lastFlush = time.Now().Add(-2 * flushEvery) // due

	for i := 0; i < 500; i++ {
		u.record(tn.MetricEvent{Event: "llm", Status: "ok", Model: "m", PromptTokens: 1, CompletionTokens: 1, Ms: 1})
		u.record(tn.MetricEvent{Event: "tool", Tool: "bash", Ms: 1})
	}
	if writes != 1 {
		t.Fatalf("1000 events inside one flush window wrote %d times, want 1", writes)
	}
	if last["llmCalls"].(int) != 1 {
		t.Fatalf("the first write should carry the first event only, got %v", last["llmCalls"])
	}

	snap, turns := u.final()
	if snap["llmCalls"].(int) != 500 || snap["toolCalls"].(int) != 500 || turns != 500 {
		t.Fatalf("final snapshot = %v turns=%d", snap, turns)
	}
	models := snap["models"].(map[string]any)
	if models["m"].(modelUsage).Calls != 500 {
		t.Fatalf("per-model breakdown = %v", models)
	}
}

// "run" events carry no tokens and must not be counted as a turn or a tool call.
func TestUsageAccumIgnoresRunEvents(t *testing.T) {
	u := newUsageAccum(pricing{Known: true}, func(map[string]any, int) {})
	u.record(tn.MetricEvent{Event: "run", Turns: 9, ToolCalls: 9})
	snap, turns := u.final()
	if turns != 0 || snap["llmCalls"].(int) != 0 || snap["toolCalls"].(int) != 0 {
		t.Fatalf("a run event was counted: %v turns=%d", snap, turns)
	}
}
