package devinadapter_test

// Live: the shipped DEFAULT provider — `opencode run -m <free Zen model>`,
// prompt on stdin — driven through toolnexus's in-built loop exactly as a step
// is, ending in a typed submit_answer. No key, no login.
//
//	DEVINADAPTER_LIVE=1 go test -run LiveOpenCode -v ./internal/devinadapter/
//
// OPENCODE_LIVE_MODEL overrides the model (default: the registry's).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	toolnexus "github.com/muthuishere/toolnexus/golang"
	devinadapter "github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
)

const liveOpenCodeModel = "opencode/ling-3.1-flash-free"

func TestLiveOpenCodeFreeModelStructuredAnswer(t *testing.T) {
	if os.Getenv("DEVINADAPTER_LIVE") != "1" {
		t.Skip("set DEVINADAPTER_LIVE=1 to run against the real opencode CLI")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not on PATH")
	}
	model := liveOpenCodeModel
	if m := os.Getenv("OPENCODE_LIVE_MODEL"); m != "" {
		model = m
	}
	a := devinadapter.New(devinadapter.Options{
		Agent:   devinadapter.OpenCode(devinadapter.CLI{Model: model}),
		Workdir: t.TempDir(),
		Timeout: 90 * time.Second,
		Repairs: 2,
		Trace: func(ex devinadapter.Exchange) {
			t.Logf("turn %d — %d byte prompt, reply:\n%s", ex.Turn, len(ex.Prompt), ex.Reply)
			if d := os.Getenv("OPENCODE_LIVE_DUMP"); d != "" {
				_ = os.WriteFile(fmt.Sprintf("%s/turn-%d-%d.txt", d, ex.Turn, len(ex.Prompt)), []byte(ex.Prompt), 0o600)
			}
		},
	})

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
	opts.SystemPrompt = "You triage software bugs. Read the report, then call submit_answer with the structured result. Severity is one of: low, medium, high, critical. Confidence is 0.0-1.0."

	start := time.Now()
	res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(), liveBugReport, tk)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatalf("no structured answer; status=%s turns=%d text=%q", res.Status, res.Turns, res.Text)
	}
	b, _ := json.MarshalIndent(got, "", "  ")
	t.Logf("%s answered in %s, %d turn(s):\n%s", model, time.Since(start).Round(time.Millisecond), res.Turns, b)
	if got.Summary == "" || got.RootCause == "" {
		t.Error("summary/root_cause not filled")
	}
}
