package catalog

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

// Price is a model's per-MILLION-token price in USD (ADR 0020).
type Price struct {
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

// Where a price came from. The UI marks a `default` price as approximate: no
// family in the table matched, so the fallback was charged.
const (
	PriceFromProvider = "provider" // pricePerMIn/Out on the registry entry
	PriceFromTable    = "table"    // the model_prices table (seeded from prices.json)
	PriceFromDefault  = "default"  // FallbackPrice: no family matched
	PriceFromLocal    = "local"    // a cli/acp program: bills no tokens
)

// DefaultPrices is the APPROXIMATE list-price table in prices.json, so a paid
// model shows a cost out of the box instead of "cost unknown". It is wrong
// within months by design (ADR 0020 "not decided here"), which is why it only
// SEEDS the model_prices table; the table is what the operator edits (wfx
// prices, the System page) and what a step is charged at. FallbackPrice is what
// a paid model no family matches is charged: a mid-tier number, the right order
// of magnitude until someone corrects it.
//
// Keys are lowercase model FAMILIES matched as a prefix of the model id's last
// path segment, longest key wins: `claude-sonnet` prices
// `anthropic/claude-sonnet-4.5`, `gpt-4o-mini` beats `gpt-4o`. A model no key
// prefixes gets FallbackPrice.
var DefaultPrices, FallbackPrice = loadSeed()

//go:embed prices.json
var seedJSON []byte

func loadSeed() (map[string]Price, Price) {
	var f struct {
		Fallback Price            `json:"fallback"`
		Prices   map[string]Price `json:"prices"`
	}
	if err := json.Unmarshal(seedJSON, &f); err != nil {
		panic("catalog/prices.json: " + err.Error())
	}
	return f.Prices, f.Fallback
}

// modelKey is the part of a model id a price is keyed on: lowercase, with any
// vendor/route prefix (`openai/`, `accounts/fireworks/models/`) dropped.
func modelKey(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// lookupPrice finds model in table: exact key, else the longest key that
// prefixes it.
func lookupPrice(table map[string]Price, model string) (Price, bool) {
	m := modelKey(model)
	if m == "" {
		return Price{}, false
	}
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if k == FallbackKey {
			continue
		}
		if nk := modelKey(k); m == nk || strings.HasPrefix(m, nk) {
			return table[k], true
		}
	}
	return Price{}, false
}

// PriceFor is the price a call to `model` on provider p is charged at, and
// where it came from, against table (the model_prices table; nil = the seed).
// The provider's own price wins when the step runs the
// provider's own model; a step that overrides `model:` is priced from the seed
// table for THAT model, since the entry's number was set for a different one.
// A cli/acp program bills no tokens: a known $0.00. ok=false (unknown) is left
// only for a step with no provider and no model at all.
func PriceFor(p Provider, model string, table map[string]Price) (Price, string, bool) {
	if p.Kind == KindCLI || p.Kind == KindACP {
		return Price{}, PriceFromLocal, true
	}
	if (model == "" || model == p.Model) && (p.PricePerMIn != nil || p.PricePerMOut != nil) {
		return Price{deref(p.PricePerMIn), deref(p.PricePerMOut)}, PriceFromProvider, true
	}
	if model == "" {
		model = p.Model
	}
	if model == "" {
		return Price{}, "", false
	}
	if isLoopback(p.BaseURL) || strings.HasSuffix(strings.ToLower(model), ":free") {
		return Price{}, PriceFromLocal, true
	}
	if table == nil {
		table = DefaultPrices
	}
	if pr, ok := lookupPrice(table, model); ok {
		return pr, PriceFromTable, true
	}
	if pr, ok := table[FallbackKey]; ok {
		return pr, PriceFromDefault, true
	}
	return FallbackPrice, PriceFromDefault, true
}

// FallbackKey is the table row holding the fallback price, so the operator can
// change it like any other.
const FallbackKey = "*"

// Seed is the whole seed as table rows: every family plus the fallback.
func Seed() map[string]Price {
	out := make(map[string]Price, len(DefaultPrices)+1)
	for k, v := range DefaultPrices {
		out[k] = v
	}
	out[FallbackKey] = FallbackPrice
	return out
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func isLoopback(u string) bool {
	u = strings.ToLower(u)
	for _, h := range []string{"://localhost", "://127.", "://[::1]", "://0.0.0.0"} {
		if strings.Contains(u, h) {
			return true
		}
	}
	return false
}
