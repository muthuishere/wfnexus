//go:build toolnexus_inprocess

// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag transport.go breaks `go build ./...` for the whole
// module, and the package is tagged as a unit so its tests keep compiling.
// Nothing is deleted:
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// Drop the tag once a toolnexus release carries the export.

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
	"path/filepath"
	"strings"
	"testing"
	"time"

	devinadapter "github.com/muthuishere/bug-fixer-platform/apps/api/internal/devinadapter"
	toolnexus "github.com/muthuishere/toolnexus/golang"
)

const liveBugReport = `# Bug: cart total is wrong when a coupon is applied twice

Steps: add item ($100), apply coupon SAVE10, apply SAVE10 again.
Expected: $90. Actual: $81.

Relevant code: apps/api/internal/engine/pricing.go — applyCoupon() appends to
order.Discounts without checking whether the coupon code is already present,
and total() folds every entry in the slice.
`

// liveModel is the model this devin account actually serves. Override with
// DEVINADAPTER_MODEL. exec passes it as one argv element, so the space in the
// name needs no quoting.
const liveModel = "SWE-1.6 Slow"

func liveModelName() string {
	if m := os.Getenv("DEVINADAPTER_MODEL"); m != "" {
		return m
	}
	return liveModel
}

func liveAgent(t *testing.T) *devinadapter.Adapter {
	t.Helper()
	if os.Getenv("DEVINADAPTER_LIVE") != "1" {
		t.Skip("set DEVINADAPTER_LIVE=1 to run against the real devin CLI")
	}
	if _, err := exec.LookPath("devin"); err != nil {
		t.Skipf("devin not on PATH: %v", err)
	}
	return devinadapter.New(devinadapter.Options{
		Agent:   devinadapter.Devin(devinadapter.CLI{Model: liveModelName()}),
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

	opts := a.InProcessOptions()
	opts.MaxTurns = 4
	opts.SystemPrompt = "You triage software bugs. Read the report, reason about the root cause, then call submit_answer with the structured result. Severity is one of: low, medium, high, critical. Confidence is 0.0-1.0."

	res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), liveBugReport, tk)
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
		SkillsDir:  []string{"./testdata/skills"},
		ExtraTools: []toolnexus.Tool{submitTool(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	a := devinadapter.New(devinadapter.Options{
		Agent:   devinadapter.Devin(devinadapter.CLI{Model: liveModelName()}),
		Workdir: t.TempDir(),
		Timeout: 8 * time.Minute,
		Trace: func(ex devinadapter.Exchange) {
			if strings.Contains(ex.Prompt, "bug-triage") {
				sawCatalog = true
			}
			t.Logf("turn %d reply:\n%s", ex.Turn, ex.Reply)
		},
	})
	opts := a.InProcessOptions()
	opts.MaxTurns = 6
	opts.SystemPrompt = "You triage software bugs. Load the bug-triage skill, follow it exactly, then call submit_answer."

	res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), liveBugReport, tk)
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

// Live: a real MCP server plus a skill plus a native tool, answered by the real
// devin CLI choosing its own calls. The scripted tests prove the wiring; this
// proves a real model can actually drive it.
func TestLiveAllSources(t *testing.T) {
	a := liveAgent(t)

	var pinged int
	upstream, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		Builtins: false,
		ExtraTools: []toolnexus.Tool{
			toolnexus.NativeTool("service_owner", "Returns which team owns a service.",
				toolnexus.JSONSchema{
					"type":       "object",
					"properties": map[string]any{"service": map[string]any{"type": "string"}},
					"required":   []string{"service"},
				},
				func(_ context.Context, args map[string]any) (string, error) {
					pinged++
					return `{"service":"pricing","owner":"payments-team","oncall":"ravi"}`, nil
				}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := upstream.Serve("127.0.0.1:0", toolnexus.ServeOptions{
		MCP: &toolnexus.MCPServeConfig{Name: "directory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Stop()

	// Builtins stay ON — the default. The model gets toolnexus's own
	// shell/file tools alongside everything else, and must use THOSE rather
	// than the CLI's native ones (the contract forbids its own), so the file
	// below only appears if a toolnexus builtin really executed.
	note := filepath.Join(t.TempDir(), "oncall.txt")

	var got *Triage
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		SkillsDir:  []string{"./testdata/skills"},
		ExtraTools: []toolnexus.Tool{submitTool(&got)},
		McpConfig: toolnexus.McpConfig{
			"directory": {Type: "remote", URL: handle.URL + "/mcp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	opts := a.InProcessOptions()
	opts.MaxTurns = 8
	opts.MaxTurns = 10
	opts.SystemPrompt = "You triage bugs. In order: load the bug-triage skill; look up who owns the 'pricing' service with the directory tool; use the `write` tool to write the on-call name to " + note + "; then call submit_answer. Put the owning team in notes."

	res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), liveBugReport, tk)
	if err != nil {
		t.Fatal(err)
	}

	used := map[string]bool{}
	for _, c := range res.ToolCalls {
		used[c.Name] = true
		t.Logf("called %s -> %.80s", c.Name, c.Output)
	}
	if !used["skill"] {
		t.Error("the model never loaded the skill")
	}
	if pinged == 0 {
		t.Error("the MCP server's tool was never called")
	}
	if b, err := os.ReadFile(note); err != nil {
		t.Errorf("no builtin wrote the file: %v", err)
	} else if !strings.Contains(strings.ToLower(string(b)), "ravi") {
		t.Errorf("the builtin wrote %q, which does not carry the MCP result", b)
	}
	if got == nil {
		t.Fatalf("no structured answer; status=%s turns=%d", res.Status, res.Turns)
	}
	if !strings.Contains(strings.ToLower(got.Notes+got.Summary+got.RootCause), "payments") {
		t.Errorf("the MCP result never made it into the answer: %+v", got)
	}
}

// Live A/B: the identical multi-turn workload on the one-shot CLI and on the
// persistent ACP session. Asserts correctness on both and that ACP is the
// faster of the two — the reason the backend exists.
func TestLiveACPIsFasterThanOneShot(t *testing.T) {
	liveAgent(t) // skip guard

	run := func(name string, back devinadapter.Agent) (time.Duration, int, *Triage) {
		var got *Triage
		tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
			Builtins:   false,
			SkillsDir:  []string{"./testdata/skills"},
			ExtraTools: []toolnexus.Tool{submitTool(&got)},
		})
		if err != nil {
			t.Fatal(err)
		}
		a := devinadapter.New(devinadapter.Options{Agent: back, Workdir: t.TempDir()})
		opts := a.InProcessOptions()
		opts.MaxTurns = 6
		opts.SystemPrompt = "You triage bugs. Load the bug-triage skill, then call submit_answer."

		start := time.Now()
		res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), liveBugReport, tk)
		took := time.Since(start)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got == nil {
			t.Fatalf("%s: no structured answer (status=%s turns=%d)", name, res.Status, res.Turns)
		}
		t.Logf("%-9s %6.1fs over %d turns, severity=%s", name, took.Seconds(), res.Turns, got.Severity)
		return took, res.Turns, got
	}

	oneShot, oneShotTurns, _ := run("one-shot", devinadapter.Devin(devinadapter.CLI{Model: liveModelName()}))

	acp := devinadapter.NewACP(devinadapter.ACP{Model: liveModelName()})
	defer acp.Close()
	acpTook, acpTurns, _ := run("acp", acp)

	t.Logf("one-shot %.1fs/%d turns = %.1fs per turn", oneShot.Seconds(), oneShotTurns, oneShot.Seconds()/float64(max(oneShotTurns, 1)))
	t.Logf("acp      %.1fs/%d turns = %.1fs per turn", acpTook.Seconds(), acpTurns, acpTook.Seconds()/float64(max(acpTurns, 1)))

	if acpTook >= oneShot {
		t.Errorf("ACP (%.1fs) was not faster than one-shot (%.1fs)", acpTook.Seconds(), oneShot.Seconds())
	}
}

// A warm ACP session answers far faster than a cold one — the property the
// whole backend rests on.
func TestLiveACPWarmsUp(t *testing.T) {
	liveAgent(t) // skip guard

	acp := devinadapter.NewACP(devinadapter.ACP{Model: liveModelName()})
	defer acp.Close()

	var times []time.Duration
	for i, word := range []string{"PONG", "PING", "PANG"} {
		start := time.Now()
		out, err := acp.Execute(context.Background(), devinadapter.Turn{
			Index: i + 1, Attempt: 1, Workdir: t.TempDir(),
			Prompt: "Reply with exactly this one word and nothing else: " + word,
		})
		if err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		times = append(times, took)
		t.Logf("prompt %d: %5.1fs -> %q", i+1, took.Seconds(), strings.TrimSpace(out))
		if !strings.Contains(strings.ToUpper(out), word) {
			t.Errorf("prompt %d: %q does not contain %s", i+1, out, word)
		}
	}
	if times[2] >= times[0] {
		t.Errorf("no warm-up: first %.1fs, third %.1fs", times[0].Seconds(), times[2].Seconds())
	}
}

// The ACP client is not devin-specific: the same code, unchanged, against
// `opencode acp`. If this passes, presets are argv + quirks, not protocol.
func TestLiveOpencodeACP(t *testing.T) {
	if os.Getenv("DEVINADAPTER_LIVE") != "1" {
		t.Skip("set DEVINADAPTER_LIVE=1")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode not on PATH: %v", err)
	}

	acp := devinadapter.NewACP(devinadapter.ACP{Bin: "opencode"})
	defer acp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	start := time.Now()
	out, err := acp.Execute(ctx, devinadapter.Turn{
		Index: 1, Attempt: 1, Workdir: t.TempDir(),
		Prompt: "Reply with exactly this one word and nothing else: PONG",
	})
	if err != nil {
		t.Fatalf("opencode acp: %v", err)
	}
	t.Logf("opencode answered in %.1fs: %.80q", time.Since(start).Seconds(), strings.TrimSpace(out))
	if !strings.Contains(strings.ToUpper(out), "PONG") {
		t.Errorf("unexpected reply: %q", out)
	}

	// Second prompt on the warm session.
	start = time.Now()
	out2, err := acp.Execute(ctx, devinadapter.Turn{
		Index: 2, Attempt: 1, Workdir: t.TempDir(),
		Prompt: "Reply with exactly this one word and nothing else: PING",
	})
	if err != nil {
		t.Fatalf("second prompt: %v", err)
	}
	t.Logf("second prompt in %.1fs: %.80q", time.Since(start).Seconds(), strings.TrimSpace(out2))
}
