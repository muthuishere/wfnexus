package catalog

import "testing"

func TestPriceForAgainstTheTable(t *testing.T) {
	http := func(base, model string) Provider { return Provider{Kind: KindHTTP, BaseURL: base, Model: model} }
	for _, c := range []struct {
		p    Provider
		want Price
		src  string
	}{
		{http("https://openrouter.ai/api/v1", "anthropic/claude-sonnet-4.5"), Price{3, 15}, PriceFromTable},
		{http("https://openrouter.ai/api/v1", "openai/gpt-4o-mini"), Price{0.15, 0.6}, PriceFromTable}, // longest key beats gpt-4o
		{http("https://api.deepseek.com", "deepseek-flash"), Price{0.28, 0.42}, PriceFromTable},
		{http("https://api.x.ai/v1", "some-new-model"), FallbackPrice, PriceFromDefault}, // never unknown
		{http("http://localhost:11434/v1", "qwen3:4b"), Price{}, PriceFromLocal},         // own hardware
		{http("https://openrouter.ai/api/v1", "meta-llama/llama-3.3-70b:free"), Price{}, PriceFromLocal},
		{Provider{Kind: KindCLI}, Price{}, PriceFromLocal},
	} {
		pr, src, ok := PriceFor(c.p, "", nil)
		if !ok || pr != c.want || src != c.src {
			t.Errorf("%s %s = %v %s %v, want %v %s", c.p.BaseURL, c.p.Model, pr, src, ok, c.want, c.src)
		}
	}
}

func TestPriceForUsesTheLiveTableAndItsFallbackRow(t *testing.T) {
	table := map[string]Price{"claude-sonnet": {2, 9}, FallbackKey: {7, 7}}
	p := Provider{Kind: KindHTTP, BaseURL: "https://x", Model: "claude-sonnet-4.5"}
	if pr, _, _ := PriceFor(p, "", table); pr != (Price{2, 9}) {
		t.Fatalf("edited row not used: %v", pr)
	}
	if pr, _, _ := PriceFor(p, "mystery-1", table); pr != (Price{7, 7}) {
		t.Fatalf("fallback row not used: %v", pr)
	}
}

func TestPriceForProviderWinsOnlyForItsOwnModel(t *testing.T) {
	in, out := 9.0, 9.0
	p := Provider{Kind: KindHTTP, Model: "claude-sonnet-4.5", PricePerMIn: &in, PricePerMOut: &out}
	if pr, src, _ := PriceFor(p, "", nil); pr != (Price{9, 9}) || src != PriceFromProvider {
		t.Fatalf("own model: %v %s", pr, src)
	}
	if pr, src, _ := PriceFor(p, "claude-haiku-4.5", nil); pr != (Price{1, 5}) || src != PriceFromTable {
		t.Fatalf("overridden model: %v %s", pr, src)
	}
}

func TestSeedCarriesTheFallback(t *testing.T) {
	if s := Seed(); s[FallbackKey] != FallbackPrice || len(s) != len(DefaultPrices)+1 {
		t.Fatalf("seed = %d rows, fallback %v", len(s), s[FallbackKey])
	}
}
