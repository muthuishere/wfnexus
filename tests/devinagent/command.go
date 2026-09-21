package devinadapter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	// OutputFile, when non-empty, is read instead of stdout after a successful
	// run — for a CLI that writes its final answer to a file (codex's
	// --output-last-message). PlaceholderFile-style substitution does not
	// apply; give a concrete path. Falls back to stdout when the file is
	// missing or empty.
	OutputFile string
}

// Devin is the CLI preset for `devin`. The prompt travels by --prompt-file, so
// it is never subject to an argv length limit.
func Devin(c CLI) *CommandAgent {
	args := []string{"--prompt-file", PlaceholderFile, "-p", "--permission-mode", c.permissionMode()}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return &CommandAgent{
		Label: "devin",
		Bin:   c.bin("devin"),
		Args:  append(args, c.ExtraArgs...),
		Env:   c.Env,
	}
}

// Claude is the CLI preset for `claude -p`.
func Claude(c CLI) *CommandAgent {
	args := []string{"-p", PlaceholderPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return &CommandAgent{
		Label: "claude",
		Bin:   c.bin("claude"),
		Args:  append(args, c.ExtraArgs...),
		Env:   c.Env,
	}
}

// Copilot is the CLI preset for `copilot -p`.
func Copilot(c CLI) *CommandAgent {
	args := []string{"-p", PlaceholderPrompt, "--log-level", "none", "--no-color"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return &CommandAgent{
		Label: "copilot",
		Bin:   c.bin("copilot"),
		Args:  append(args, c.ExtraArgs...),
		Env:   c.Env,
	}
}

// CLI is the small shared config the presets take.
type CLI struct {
	// Bin overrides the executable name.
	Bin string
	// Model is passed to the CLI's model flag. "" ⇒ the account default.
	Model string
	// PermissionMode applies to CLIs that have one (devin). "" ⇒ "auto".
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

func (c CLI) permissionMode() string {
	if c.PermissionMode != "" {
		return c.PermissionMode
	}
	return "auto"
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

	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = t.Workdir
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
