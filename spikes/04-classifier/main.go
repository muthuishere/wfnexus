// Spike 04 — tn.Classifier, the judge tier our `decide:` block uses.
//
//	(a) StyleSystemOne over https://openrouter.ai/api/v1, model typesafe/jev-1.13
//	(b) StyleLLM over the same base with the cheap chat model
//	(c) StyleStatic from a recorded decision — must need NO network (CI)
//	(d) the ENCODING CONTROL: the same choice, options described only by their id
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"
)

var ctx = context.Background()

// the state every backend is asked about
var bug = map[string]any{
	"title": "Checkout returns 500 for coupon codes with a trailing space",
	"body": "Since the 4.2 release, POST /api/checkout returns HTTP 500 when the coupon " +
		"code has a trailing space. Stack trace points at pricing/coupon.go:88, " +
		"strconv.Atoi on an untrimmed string. Affects ~4% of checkouts. " +
		"Reproduced on staging with curl.",
	"reporter": "support-team",
}

func questions(byConsequence bool) map[string]tn.Question {
	component := map[string]string{
		"pricing":  "the defect is in coupon/discount calculation code and a pricing engineer should own the fix",
		"checkout": "the defect is in the HTTP checkout handler or its request validation",
		"frontend": "the defect is in the browser client and no server change is needed",
		"infra":    "the defect is in deployment, config or capacity and no application code changes",
	}
	if !byConsequence {
		// the control: an option described only by its own id (ADR-0021 D1)
		component = map[string]string{
			"pricing": "pricing", "checkout": "checkout", "frontend": "frontend", "infra": "infra",
		}
	}
	return map[string]tn.Question{
		"actionable": tn.NoulQuestion{
			Instructions: "This bug report contains enough concrete detail for an engineer to start work without asking the reporter anything.",
			Criteria: &tn.NoulCriteria{
				True:  "it names a reproducible trigger, an endpoint or file, and an observable failure",
				False: "it is vague, asks a question, or is a feature request with no failure described",
			},
		},
		"component": tn.ChoiceQuestion{
			Instructions: "Which component owns the fix for this bug report?",
			Criteria:     component,
		},
		"risk": tn.ScoreQuestion{
			Instructions: "How risky is it to let an autonomous agent fix this without human review?",
			Criteria: []string{
				"cosmetic: a typo or comment, no behaviour changes",
				"local: one pure function, fully covered by tests",
				"moderate: touches request handling or shared helpers",
				"high: touches money, auth, or data migration",
				"critical: irreversible or outward-facing side effects",
			},
		},
	}
}

func main() {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		fmt.Println("OPENROUTER_API_KEY unset")
		os.Exit(1)
	}
	model := envOr("SPIKE_MODEL", "anthropic/claude-haiku-4.5")
	qs := questions(true)

	// ---------------------------------------------------------- (a) systemone
	fmt.Println("== (a) StyleSystemOne over openrouter, model typesafe/jev-1.13 ==")
	so, err := tn.CreateClassifier(tn.ClassifierOptions{
		Style:     tn.StyleSystemOne,
		BaseURL:   "https://openrouter.ai/api/v1",
		Model:     "typesafe/jev-1.13",
		APIKeyEnv: "OPENROUTER_API_KEY",
		Timeout:   20 * time.Second,
	})
	if err != nil {
		fmt.Printf("CreateClassifier: %v\n", err)
	} else {
		t0 := time.Now()
		d, err := so.Evaluate(ctx, bug, qs)
		fmt.Printf("latency: %s\n", time.Since(t0).Round(time.Millisecond))
		if err != nil {
			fmt.Printf("REACHABLE? NO — Evaluate error: %v\n", err)
			fmt.Printf("  (systemone posts to BaseURL + \"/systemone\" = %s)\n",
				"https://openrouter.ai/api/v1/systemone")
		} else {
			report("systemone", d)
		}
	}
	// control: the SAME request against typesafe's own default base
	fmt.Println("\n-- control: the same call against the DEFAULT base (api.typesafe.ai) --")
	so2, _ := tn.CreateClassifier(tn.ClassifierOptions{
		Style: tn.StyleSystemOne, Model: "jev-1.13",
		APIKeyEnv: "TYPESAFE_API_KEY", Timeout: 15 * time.Second,
	})
	t0 := time.Now()
	if d, err := so2.Evaluate(ctx, bug, qs); err != nil {
		fmt.Printf("latency: %s  error: %v\n", time.Since(t0).Round(time.Millisecond), err)
	} else {
		report("systemone@typesafe", d)
	}

	// ---------------------------------------------------------- (b) llm
	fmt.Println("\n== (b) StyleLLM over openrouter with " + model + " ==")
	chat := tn.CreateClient(tn.ClientOptions{
		BaseURL: "https://openrouter.ai/api/v1", Style: tn.StyleOpenAI,
		Model: model, APIKey: key, MaxTurns: 1,
	})
	llmC, err := tn.CreateClassifier(tn.ClassifierOptions{
		Style: tn.StyleLLM, Model: model, Client: chat,
	})
	must(err)
	t0 = time.Now()
	dLLM, err := llmC.Evaluate(ctx, bug, qs)
	fmt.Printf("latency: %s\n", time.Since(t0).Round(time.Millisecond))
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
	} else {
		report("llm", dLLM)
	}

	// ---------------------------------------------------------- (d) encoding control
	fmt.Println("\n== (d) ENCODING CONTROL: same choice, options described BY ID ONLY ==")
	qsCtl := questions(false)
	t0 = time.Now()
	dCtl, err := llmC.Evaluate(ctx, bug, qsCtl)
	fmt.Printf("latency: %s\n", time.Since(t0).Round(time.Millisecond))
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
	} else {
		report("llm/by-id", dCtl)
		if a, e := dLLM.Choice("component"); e == nil {
			if b, e2 := dCtl.Choice("component"); e2 == nil {
				fmt.Printf("  DELTA  by-consequence %s p=%.2f nearUniform=%v  |  by-id %s p=%.2f nearUniform=%v\n",
					a.Choice, a.Probabilities[a.Choice], a.NearUniform,
					b.Choice, b.Probabilities[b.Choice], b.NearUniform)
			}
		}
	}

	// ---------------------------------------------------------- (c) static
	fmt.Println("\n== (c) StyleStatic — the CI backend, NO network ==")
	recorded := []byte(`{"model":"jev-1.13","calibrated":true,` +
		`"usage":{"input_tokens":411,"output_tokens":37},` +
		`"answers":{` +
		`"actionable":{"type":"noul","noul":0.94},` +
		`"component":{"type":"choice","choice":"pricing","probabilities":{"pricing":0.81,"checkout":0.15,"frontend":0.02,"infra":0.02},"confidence":0.81},` +
		`"risk":{"type":"score","score":1.4,"legend":{"0":"cosmetic","1":"local","2":"moderate","3":"high","4":"critical"},"probabilities":{"0":0.05,"1":0.55,"2":0.32,"3":0.06,"4":0.02},"confidence":0.55}}}`)
	st, err := tn.CreateClassifier(tn.ClassifierOptions{
		Style: tn.StyleStatic, Model: "jev-1.13",
		Decisions: []tn.RecordedDecision{{State: bug, Questions: qs, Response: recorded}},
	})
	must(err)
	// prove it needs no network: point nothing at a wire and time it
	t0 = time.Now()
	dS, err := st.Evaluate(ctx, bug, qs)
	fmt.Printf("latency: %s (offline)\n", time.Since(t0))
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
	} else {
		report("static", dS)
	}
	// an UNRECORDED state must error, never guess
	_, errU := st.Evaluate(ctx, map[string]any{"title": "never recorded"}, qs)
	fmt.Printf("unrecorded state -> %v\n", errU)

	// ------------------------------------------- (e) the REAL encoding control
	// (d) barely moved because the option IDS are themselves meaningful English.
	// The ADR's collapse case is OPAQUE ids: same four options, no semantics in
	// the key, described by consequence vs described by id. Run on systemone.
	fmt.Println("\n== (e) OPAQUE-ID encoding control on systemone ==")
	opaqueByConsequence := map[string]string{
		"opt_a": "the defect is in coupon/discount calculation code and a pricing engineer should own the fix",
		"opt_b": "the defect is in the HTTP checkout handler or its request validation",
		"opt_c": "the defect is in the browser client and no server change is needed",
		"opt_d": "the defect is in deployment, config or capacity and no application code changes",
	}
	opaqueByID := map[string]string{"opt_a": "opt_a", "opt_b": "opt_b", "opt_c": "opt_c", "opt_d": "opt_d"}
	for _, tc := range []struct {
		tag string
		c   map[string]string
	}{{"opaque/by-consequence (opt_a IS pricing)", opaqueByConsequence}, {"opaque/by-id", opaqueByID}} {
		q := map[string]tn.Question{"component": tn.ChoiceQuestion{
			Instructions: "Which component owns the fix for this bug report?", Criteria: tc.c}}
		t := time.Now()
		d, err := so.Evaluate(ctx, bug, q)
		if err != nil {
			fmt.Printf("  [%s] ERROR %v\n", tc.tag, err)
			continue
		}
		a, _ := d.Choice("component")
		fmt.Printf("  [%s] %s choice=%q conf=%.2f nearUniform=%v probs=%s\n",
			tc.tag, time.Since(t).Round(time.Millisecond), a.Choice, a.Confidence, a.NearUniform, probs(a.Probabilities))
	}

	// the canonical request bytes: what a recording is keyed on
	canon, _ := tn.CanonicalRequest("jev-1.13", qs)
	fmt.Printf("\nCanonicalRequest bytes: %d (this + state is the static key)\n", len(canon))
}

func report(tag string, d tn.Decision) {
	cost := "nil (backend did not report one)"
	if d.Usage.Cost != nil {
		cost = fmt.Sprintf("%v", *d.Usage.Cost)
	}
	fmt.Printf("  [%s] model=%s calibrated=%v usage=in:%d/out:%d cost=%s\n",
		tag, d.Model, d.Calibrated, d.Usage.InputTokens, d.Usage.OutputTokens, cost)
	if a, err := d.Noul("actionable"); err == nil {
		fmt.Printf("    noul   actionable = %.3f\n", a.Noul)
	}
	if a, err := d.Choice("component"); err == nil {
		fmt.Printf("    choice component  = %q conf=%.2f nearUniform=%v probs=%s\n",
			a.Choice, a.Confidence, a.NearUniform, probs(a.Probabilities))
	}
	if a, err := d.Score("risk"); err == nil {
		fmt.Printf("    score  risk       = %.2f conf=%.2f levels=%v probs=%s\n",
			a.Score, a.Confidence, a.Levels(), probs(a.Probabilities))
	}
}

func probs(m map[string]float64) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b []string
	for _, k := range ks {
		b = append(b, fmt.Sprintf("%s=%.2f", k, m[k]))
	}
	return "{" + strings.Join(b, " ") + "}"
}

func must(err error) {
	if err != nil {
		b, _ := json.Marshal(err.Error())
		fmt.Println("FATAL:", string(b))
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
