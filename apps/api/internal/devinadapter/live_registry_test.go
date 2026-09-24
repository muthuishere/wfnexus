package devinadapter_test

// Live: the ARGV TEMPLATES that registries.json ships for each agent CLI,
// executed exactly as the engine builds them (CommandAgent = Bin + Command[1:]
// + Args, then ModelFlag + model appended).
//
// ADR 0016: "we do not ship an adapter whose non-interactive invocation we have
// not verified by running it." This file is where that verification lives, so a
// registry entry and its proof cannot drift apart. Every argv here was derived
// from the CLI's own `--help`, never guessed — the help output is quoted in
// docs/spikes/FINDINGS.md.
//
//	DEVINADAPTER_LIVE=1 go test -run LiveRegistry -v -timeout 20m ./internal/devinadapter/
//
// ollama is the one that needs no account and no network, so it is the entry
// this file proves end to end on any machine.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	devinadapter "github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
)

type registryArgv struct {
	provider  string // the registries.json key
	bin       string
	args      []string // Command[1:] + Args, placeholders intact
	modelFlag string
	model     string
	timeout   time.Duration
}

// liveRegistryEntries mirrors registries.json. Keep them identical: the point
// of the file is that the shipped entry is the thing that was run.
var liveRegistryEntries = []registryArgv{
	{
		provider: "ollama-cli", bin: "ollama",
		// `ollama run MODEL [PROMPT]` — the model is POSITIONAL, so it is
		// baked into the command and modelFlag is empty. ollama has no
		// --model flag at all; appending one would be a parse error.
		args:    []string{"run", "qwen3:4b", "{{prompt}}", "--hidethinking"},
		timeout: 3 * time.Minute,
	},
	{
		provider: "opencode", bin: "opencode",
		args:      []string{"run", "{{prompt}}"},
		modelFlag: "--model", model: "openrouter/anthropic/claude-haiku-4.5",
		timeout: 5 * time.Minute,
	},
	{
		provider: "codex-cli", bin: "codex",
		// `codex exec [PROMPT]` is the documented non-interactive entry.
		// --skip-git-repo-check because a turn runs in a scratch workdir that
		// is not a repo; --color never so the reply is not wrapped in escapes.
		args:      []string{"exec", "--skip-git-repo-check", "--color", "never", "{{prompt}}"},
		modelFlag: "-m",
		timeout:   5 * time.Minute,
	},
	{
		provider: "claude-cli", bin: "claude",
		args: []string{"-p", "{{prompt}}"}, modelFlag: "--model",
		timeout: 5 * time.Minute,
	},
	{
		provider: "copilot-cli", bin: "copilot",
		// --allow-all-tools is what copilot's own help calls "required for
		// non-interactive mode"; without it the run waits for a confirmation
		// nobody is there to give and dies at the timeout.
		args:      []string{"-p", "{{prompt}}", "--allow-all-tools", "--log-level", "none", "--no-color"},
		modelFlag: "--model",
		timeout:   5 * time.Minute,
	},
}

func TestLiveRegistryArgvs(t *testing.T) {
	if os.Getenv("DEVINADAPTER_LIVE") != "1" {
		t.Skip("set DEVINADAPTER_LIVE=1 to run the real CLIs")
	}
	only := os.Getenv("LIVE_PROVIDER")

	for _, e := range liveRegistryEntries {
		t.Run(e.provider, func(t *testing.T) {
			if only != "" && only != e.provider {
				t.Skipf("LIVE_PROVIDER=%s", only)
			}
			if _, err := exec.LookPath(e.bin); err != nil {
				t.Skipf("%s not on PATH: %v", e.bin, err)
			}
			a := &devinadapter.CommandAgent{
				Label: e.provider, Bin: e.bin, Args: e.args, ModelFlag: e.modelFlag,
			}
			ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
			defer cancel()

			start := time.Now()
			out, err := a.Execute(ctx, devinadapter.Turn{
				Index: 1, Attempt: 1, Workdir: t.TempDir(), Model: e.model,
				Prompt: "Reply with exactly this one word and nothing else: PONG",
			})
			if err != nil {
				t.Fatalf("%s: %v", e.provider, err)
			}
			t.Logf("%s answered in %.1fs: %.200q", e.provider, time.Since(start).Seconds(), strings.TrimSpace(out))
			if !strings.Contains(strings.ToUpper(out), "PONG") {
				t.Errorf("%s: no PONG in %q", e.provider, out)
			}
		})
	}
}
