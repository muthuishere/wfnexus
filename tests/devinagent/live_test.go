package devinadapter_test

// Live tests: the real `devin` binary, a real account, real latency.
// They are skipped unless DEVINADAPTER_LIVE=1 so `go test ./...` stays fast
// and offline.
//
//	DEVINADAPTER_LIVE=1 go test -run Live -v -timeout 20m ./...

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	devinadapter "github.com/muthuishere/devinadapter"
	toolnexus "github.com/muthuishere/toolnexus/golang"
)

const liveBugReport = `# Bug: cart total is wrong when a coupon is applied twice

Steps: add item ($100), apply coupon SAVE10, apply SAVE10 again.
Expected: $90. Actual: $81.

Relevant code: apps/api/internal/engine/pricing.go — applyCoupon() appends to
order.Discounts without checking whether the coupon code is already present,
and total() folds every entry in the slice.
`

func liveAgent(t *testing.T) *devinadapter.Adapter {
	t.Helper()
	if os.Getenv("DEVINADAPTER_LIVE") != "1" {
		t.Skip("set DEVINADAPTER_LIVE=1 to run against the real devin CLI")
	}
	if _, err := exec.LookPath("devin"); err != nil {
		t.Skipf("devin not on PATH: %v", err)
	}
	return devinadapter.New(devinadapter.Options{
		Agent:   devinadapter.Devin(devinadapter.CLI{Model: os.Getenv("DEVINADAPTER_MODEL")}),
		Workdir: t.TempDir(),
		Timeout: 8 * time.Minute,
		Trace: func(ex devinadapter.Exchange) {
			t.Logf("turn %d — %d byte prompt, reply:\n%s", ex.Turn, len(ex.Prompt), ex.Reply)
		},
	})
}

// The headline path: a file is the prompt, a struct is the answer.
func TestLiveStructuredAnswer(t *testing.T) {
	a := liveAgent(t)

	var got *Triage
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins:   false,
		ExtraTools: []toolnexus.Tool{submitTool(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	opts := a.ClientOptions()
	opts.MaxTurns = 4
	opts.SystemPrompt = "You triage software bugs. Read the report, reason about the root cause, then call submit_answer with the structured result. Severity is one of: low, medium, high, critical. Confidence is 0.0-1.0."

	res, err := toolnexus.CreateClient(opts).Run(context.Background(), liveBugReport, tk)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatalf("no structured answer; status=%s turns=%d text=%q", res.Status, res.Turns, res.Text)
	}

	b, _ := json.MarshalIndent(got, "", "  ")
	t.Logf("structured answer:\n%s", b)

	// Assert the SHAPE, not the prose — the model is free to word it its way.
	if got.Summary == "" || got.RootCause == "" {
		t.Error("summary/root_cause not filled")
	}
	switch got.Severity {
	case "low", "medium", "high", "critical":
	default:
		t.Errorf("severity %q is off the ladder", got.Severity)
	}
	if got.Confidence <= 0 || got.Confidence > 1 {
		t.Errorf("confidence %v out of range", got.Confidence)
	}
	if len(got.Files) == 0 {
		t.Error("no files named")
	}
	// The report names pricing.go; a correct triage should point at it.
	if !strings.Contains(strings.Join(got.Files, " "), "pricing.go") {
		t.Errorf("expected pricing.go among files: %v", got.Files)
	}
}

// The same run with a skills directory: the catalog must reach the CLI, and
// the skill's rule must change the answer. The bug report is money-related and
// the skill says money is never below `high` — a plain run tends to say
// `medium` (it did, in the first live run of this suite), so this asserts the
// skill actually landed rather than that the model got lucky.
func TestLiveSkillsChangeTheAnswer(t *testing.T) {
	liveAgent(t) // skip guard

	var sawCatalog bool
	var got *Triage
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins:   false,
		SkillsDir:  []string{"./skills"},
		ExtraTools: []toolnexus.Tool{submitTool(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	a := devinadapter.New(devinadapter.Options{
		Agent:   devinadapter.Devin(devinadapter.CLI{Model: os.Getenv("DEVINADAPTER_MODEL")}),
		Workdir: t.TempDir(),
		Timeout: 8 * time.Minute,
		Trace: func(ex devinadapter.Exchange) {
			if strings.Contains(ex.Prompt, "bug-triage") {
				sawCatalog = true
			}
			t.Logf("turn %d reply:\n%s", ex.Turn, ex.Reply)
		},
	})
	opts := a.ClientOptions()
	opts.MaxTurns = 6
	opts.SystemPrompt = "You triage software bugs. Load the bug-triage skill, follow it exactly, then call submit_answer."

	res, err := toolnexus.CreateClient(opts).Run(context.Background(), liveBugReport, tk)
	if err != nil {
		t.Fatal(err)
	}
	if !sawCatalog {
		t.Error("the skills catalog never reached the prompt file")
	}
	if got == nil {
		t.Fatalf("no structured answer; status=%s turns=%d text=%q", res.Status, res.Turns, res.Text)
	}

	b, _ := json.MarshalIndent(got, "", "  ")
	t.Logf("with skill:\n%s", b)
	t.Logf("tool calls: %d", res.ToolCallCount)

	// The skill's rule: a wrong charged amount is never below high.
	if got.Severity != "high" && got.Severity != "critical" {
		t.Errorf("skill's severity rule not applied: got %q", got.Severity)
	}
}

// The CLI preset really does drive the binary: no toolnexus, just the Agent.
func TestLiveCommandAgentDirect(t *testing.T) {
	if os.Getenv("DEVINADAPTER_LIVE") != "1" {
		t.Skip("set DEVINADAPTER_LIVE=1 to run against the real devin CLI")
	}
	if _, err := exec.LookPath("devin"); err != nil {
		t.Skipf("devin not on PATH: %v", err)
	}

	dir := t.TempDir()
	f := dir + "/prompt.md"
	if err := os.WriteFile(f, []byte("Reply with exactly the word: PONG"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	out, err := devinadapter.Devin(devinadapter.CLI{}).Execute(ctx, devinadapter.Turn{
		Index: 1, PromptFile: f, Prompt: "Reply with exactly the word: PONG", Workdir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("devin said: %q", out)
	if !strings.Contains(strings.ToUpper(out), "PONG") {
		t.Errorf("expected PONG, got %q", out)
	}
}
