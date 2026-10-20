// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag transport.go breaks `go build ./...` for the whole
// module, and the package is tagged as a unit so its tests keep compiling.
// Nothing is deleted:
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// Drop the tag once a toolnexus release carries the export.

package devinadapter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Devin permission modes, as the CLI names them.
const (
	// PermissionAuto auto-approves read-only tools only.
	PermissionAuto = "auto"
	// PermissionAcceptEdits also auto-approves workspace edits.
	PermissionAcceptEdits = "accept-edits"
	// PermissionSmart additionally auto-runs what a fast model judges safe.
	PermissionSmart = "smart"
	// PermissionBypass auto-approves everything — devin's "dangerous" mode.
	// This is the DEFAULT here: the adapter drives the CLI non-interactively,
	// so a prompt has no one to answer it and the turn just blocks until the
	// timeout. The contract already tells the model not to use its own tools,
	// and the work the host cares about runs in the host process, not in the
	// CLI. Set CLI.PermissionMode to narrow it.
	PermissionBypass = "dangerous"
)

// Placeholders substituted into CommandAgent.Args before exec.
const (
	// PlaceholderFile is replaced by the path of the rendered prompt file.
	PlaceholderFile = "{{file}}"
	// PlaceholderPrompt is replaced by the prompt text itself, for a CLI with
	// no file flag. Prefer PlaceholderFile: argv has a length limit and
	// quoting hazards; a file has neither.
	PlaceholderPrompt = "{{prompt}}"
)

// CommandAgent runs a local CLI in one-shot mode. It is the generic command
// adapter: give it an argv template and it drives any agent CLI that reads a
// prompt and prints an answer. Devin, Codex, Claude and Copilot are presets
// below, not special cases.
type CommandAgent struct {
	// Label is the Name() reported in traces. "" ⇒ Bin.
	Label string
	// Bin is the executable, looked up on PATH unless it is a path.
	Bin string
	// Args is the argv template. PlaceholderFile / PlaceholderPrompt are
	// substituted per turn; every other element is passed through.
	Args []string
	// Env is extra environment, "K=V", appended to the parent environment.
	Env []string
	// ModelFlag is the CLI's model flag, e.g. "--model". When set and a model
	// is known for the turn, the flag and its value are appended. An explicit
	// CLI.Model is baked into Args instead and wins, so a preset pinned to a
	// model ignores what the caller asked for.
	ModelFlag string
	// OutputFile, when non-empty, is read instead of stdout after a successful
	// run — for a CLI that writes its final answer to a file (codex's
	// --output-last-message). PlaceholderFile-style substitution does not
	// apply; give a concrete path. Falls back to stdout when the file is
	// missing or empty.
	OutputFile string
	// Stdin, when true, feeds the prompt text on standard input. It has the
	// prompt file's advantages (no argv length limit, no quoting) for a CLI
	// that has no prompt-file flag but reads stdin, like `claude -p`.
	Stdin bool
}

// Devin is the CLI preset for `devin`. The prompt travels by --prompt-file, so
// it is never subject to an argv length limit.
func Devin(c CLI) *CommandAgent {
	args := []string{"--prompt-file", PlaceholderFile, "-p", "--permission-mode", c.permissionMode()}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return &CommandAgent{
		Label:     "devin",
		Bin:       c.bin("devin"),
		Args:      append(args, c.ExtraArgs...),
		Env:       c.Env,
		ModelFlag: c.modelFlag("--model"),
	}
}

// ClaudeArgs is the fixed part of the `claude` preset's argv:
//
//   - -p: print mode, one turn, no TTY.
//   - --tools "": no built-in tools. The adapter's contract has the model
//     answer with tool calls the HOST executes (skills included — toolnexus
//     puts the step's skill catalogue and its `skill` tool in the request), so
//     claude's own Bash/Edit would only act outside the step's guardrails. It
//     also settles the headless permission question: with nothing to approve
//     nothing blocks on a prompt, and no bypass flag is needed.
//   - --disable-slash-commands: the operator's personal ~/.claude skills stay
//     out of a workflow step; the step's own skills arrive in the request.
//   - --no-session-persistence: a turn is not a resumable chat session.
//
// The prompt itself goes on stdin (CommandAgent.Stdin), never argv.
var ClaudeArgs = []string{"-p", "--tools", "", "--disable-slash-commands", "--no-session-persistence"}

// Claude is the CLI preset for `claude -p`.
func Claude(c CLI) *CommandAgent {
	args := append([]string{}, ClaudeArgs...)
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return &CommandAgent{
		Label:     "claude",
		Bin:       c.bin("claude"),
		Args:      append(args, c.ExtraArgs...),
		Env:       c.Env,
		ModelFlag: c.modelFlag("--model"),
		Stdin:     true,
	}
}

// Copilot is the CLI preset for `copilot -p`.
func Copilot(c CLI) *CommandAgent {
	args := []string{"-p", PlaceholderPrompt, "--log-level", "none", "--no-color"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return &CommandAgent{
		Label:     "copilot",
		Bin:       c.bin("copilot"),
		Args:      append(args, c.ExtraArgs...),
		Env:       c.Env,
		ModelFlag: c.modelFlag("--model"),
	}
}

// OpenCode is the CLI preset for `opencode run`: one fresh process per turn,
// prompt on stdin (verified: `echo … | opencode run -m <model>` answers on a
// clean stdout, the "> build · <model>" banner goes to stderr; a bad model
// exits 1 with "Model unavailable"). This is the path opencode Zen's FREE
// models still accept: over ACP they now refuse with "OpenCode's free tier
// can only be used from within OpenCode" (2026-10-14).
//
// --standalone runs a private server per turn instead of opencode v2's
// background service, which sits on one fixed port (127.0.0.1:49374) for the
// whole MACHINE: on a box where another user already runs it, every call
// failed with "Managed service port 49374 … already in use" (prod-db,
// 2026-10-20). A private server also keeps two steps from sharing one.
func OpenCode(c CLI) *CommandAgent {
	args := []string{"run", "--standalone"}
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	return &CommandAgent{
		Label:     "opencode",
		Bin:       c.bin("opencode"),
		Args:      append(args, c.ExtraArgs...),
		Env:       c.Env,
		ModelFlag: c.modelFlag("-m"),
		Stdin:     true,
	}
}

// CLI is the small shared config the presets take.
type CLI struct {
	// Bin overrides the executable name.
	Bin string
	// Model is passed to the CLI's model flag. "" ⇒ the account default.
	Model string
	// PermissionMode applies to CLIs that have one (devin).
	// "" ⇒ PermissionBypass.
	PermissionMode string
	// ExtraArgs are appended to the template.
	ExtraArgs []string
	// Env is extra environment, "K=V".
	Env []string
}

func (c CLI) bin(def string) string {
	if c.Bin != "" {
		return c.Bin
	}
	return def
}

// modelFlag returns the flag only when the preset did NOT pin a model — a
// pinned model is already in Args and must not be overridden per turn.
func (c CLI) modelFlag(flag string) string {
	if c.Model != "" {
		return ""
	}
	return flag
}

func (c CLI) permissionMode() string {
	if c.PermissionMode != "" {
		return c.PermissionMode
	}
	return PermissionBypass
}

// Name implements Agent.
func (c *CommandAgent) Name() string {
	if c.Label != "" {
		return c.Label
	}
	return c.Bin
}

// Execute implements Agent: substitute the template, run, return the reply.
func (c *CommandAgent) Execute(ctx context.Context, t Turn) (string, error) {
	args := make([]string, 0, len(c.Args))
	for _, a := range c.Args {
		a = strings.ReplaceAll(a, PlaceholderFile, t.PromptFile)
		a = strings.ReplaceAll(a, PlaceholderPrompt, t.Prompt)
		args = append(args, a)
	}

	// The model toolnexus asked for, when the preset left the choice open.
	if c.ModelFlag != "" && t.Model != "" {
		args = append(args, c.ModelFlag, t.Model)
	}

	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = t.Workdir
	if c.Stdin {
		cmd.Stdin = strings.NewReader(t.Prompt)
	}
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", c.Name(), err, strings.TrimSpace(stderr.String()))
	}
	if c.OutputFile != "" {
		if b, err := os.ReadFile(c.OutputFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
			return string(bytes.TrimSpace(b)), nil
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}
