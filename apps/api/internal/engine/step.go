package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/store"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

const maxEventOutput = 4000

// executeStep runs ONE workflow step as a toolnexus agent and returns its
// schema-validated output.
//
// The output contract is enforced the toolnexus-native way: the step's JSON
// schema becomes the input schema of a native `submit_output` tool. The model
// must call it; the tool validates and either accepts (captured) or returns the
// validation errors as the tool result so the model self-corrects inside the
// loop. A Completion gate then refuses to finish until a submission was accepted.
func (e *Engine) executeStep(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData) (map[string]any, error) {
	prompt, err := workflow.Render(step.Prompt, data)
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}
	system, err := workflow.Render(step.System, data)
	if err != nil {
		return nil, fmt.Errorf("render system: %w", err)
	}
	e.setStep(ctx, runID, step.ID, store.StepPatch{Status: str("running"), Prompt: str(prompt), StartedAt: now(), Error: str("")})

	schema, err := compileSchema(def.Name+"/"+step.ID, step.OutputSchema)
	if err != nil {
		return nil, fmt.Errorf("output_schema: %w", err)
	}

	tk, err := e.buildToolkit(ctx, step)
	if err != nil {
		return nil, err
	}
	defer tk.Close()

	var submitted map[string]any
	tk.Register(tn.NativeTool("submit_output",
		"Submit the FINAL result of this step as JSON matching the required schema. Call exactly once when the work is complete. If it returns validation errors, fix them and call again.",
		step.OutputSchema,
		func(_ context.Context, args map[string]any) (string, error) {
			if err := validateJSON(schema, args); err != nil {
				return "", err
			}
			submitted = args
			return "accepted", nil
		}))

	model := step.Model
	if model == "" {
		model = e.cfg.Model
	}
	opts := tn.ClientOptions{
		BaseURL:      e.cfg.LLMBaseURL,
		Style:        tn.ClientStyle(e.cfg.LLMStyle),
		Model:        model,
		SystemPrompt: e.systemPrompt(system, data),
		MaxTurns:     step.MaxTurns,
		TimeoutMs:    step.TimeoutSec * 1000,
		Hooks:        e.hooks(ctx, runID, step.ID, data.WorkDir),
		OnMetric: func(m tn.MetricEvent) {
			e.emit(ctx, runID, step.ID, "metric", m)
		},
	}

	ag := agents.New(step.ID, agents.Spec{
		Does:  step.Description,
		Tools: tk.Tools(),
		Model: model,
		Completion: &agents.Completion{
			MaxAttempts: step.MaxAttempts,
			Verify: func(r tn.RunResult) (bool, string) {
				if submitted != nil {
					return true, ""
				}
				return false, "You have not called submit_output with an accepted result yet. Finish the work and call submit_output with JSON matching the schema."
			},
		},
		Budget: &agents.Budget{MaxTurns: step.MaxTurns},
	})

	e.emit(ctx, runID, step.ID, "log", map[string]any{"text": fmt.Sprintf("agent %s → %s (skills=%v tools=%v)", step.ID, model, step.Skills, step.Tools)})
	outcome, err := ag.Loop(opts, tk).Run(ctx, prompt, agents.RunOpts{})
	patch := store.StepPatch{Attempts: intp(outcome.Attempts), Turns: intp(outcome.Turns), RawText: str(outcome.Text), Usage: mustJSON(outcome.Result.Usage)}
	e.setStep(ctx, runID, step.ID, patch)
	e.saveArtifacts(ctx, runID, step.ID, data.WorkDir, outcome)
	if err != nil {
		return nil, err
	}
	if submitted == nil {
		return nil, fmt.Errorf("step %s stopped without a valid result: %s (%s)", step.ID, outcome.Status, outcome.StoppedBy)
	}
	return submitted, nil
}

func (e *Engine) systemPrompt(stepSystem string, data workflow.TemplateData) string {
	var b strings.Builder
	b.WriteString("You are one step of an automated bug-fixing workflow. Work autonomously; do not ask the user questions unless a tool named `question` is available.\n")
	if data.WorkDir != "" {
		fmt.Fprintf(&b, "The repository under test is checked out at: %s\nRun every shell command with workdir=%q and use absolute paths under it for file tools.\n", data.WorkDir, data.WorkDir)
	}
	b.WriteString("When your work is complete, call `submit_output` exactly once with JSON that matches its schema. That call is the ONLY way your result is recorded.\n")
	if stepSystem != "" {
		b.WriteString("\n")
		b.WriteString(stepSystem)
	}
	return b.String()
}

// buildToolkit assembles the per-step toolkit: skills allowlist, builtin allowlist, MCP server allowlist.
func (e *Engine) buildToolkit(ctx context.Context, step *workflow.Step) (*tn.Toolkit, error) {
	opts := tn.Options{}
	if len(step.Skills) > 0 {
		opts.SkillsDir = []string{e.cfg.SkillsDir}
		opts.SkillsFilter = map[string]bool{}
		for _, s := range step.Skills {
			opts.SkillsFilter[s] = true
		}
	}
	bt := tn.BuiltinsConfig{Tools: map[string]bool{}}
	if len(step.Tools) == 0 {
		f := false
		bt.Enabled = &f
	}
	for _, t := range step.Tools {
		bt.Tools[t] = true
	}
	opts.Builtins = bt
	if len(step.MCP) > 0 {
		raw, err := os.ReadFile(e.cfg.McpConfig)
		if err != nil {
			return nil, fmt.Errorf("mcp config: %w", err)
		}
		var all map[string]any
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, fmt.Errorf("mcp config: %w", err)
		}
		servers, _ := all["mcpServers"].(map[string]any)
		keep := map[string]any{}
		for _, name := range step.MCP {
			if s, ok := servers[name]; ok {
				keep[name] = s
			} else {
				return nil, fmt.Errorf("step %s: mcp server %q not in %s", step.ID, name, e.cfg.McpConfig)
			}
		}
		opts.McpConfig = map[string]any{"mcpServers": keep}
	}
	return tn.CreateToolkit(ctx, opts)
}

// hooks stream the agent's tool activity into the run event log and pin bash to the workspace.
func (e *Engine) hooks(ctx context.Context, runID uuid.UUID, stepID, workdir string) *tn.Hooks {
	return &tn.Hooks{
		BeforeTool: func(_ context.Context, ev tn.BeforeToolEvent) (*tn.ToolOverride, error) {
			var ov *tn.ToolOverride
			if ev.Name == "bash" && workdir != "" {
				if wd, _ := ev.Args["workdir"].(string); wd == "" {
					args := map[string]any{}
					for k, v := range ev.Args {
						args[k] = v
					}
					args["workdir"] = workdir
					ov = &tn.ToolOverride{Args: args}
					ev.Args = args
				}
			}
			e.emit(ctx, runID, stepID, "tool_call", map[string]any{"name": ev.Name, "id": ev.ID, "turn": ev.Turn, "args": ev.Args})
			return ov, nil
		},
		AfterTool: func(_ context.Context, ev tn.AfterToolEvent) (*tn.ToolOverride, error) {
			out := ev.Result.Output
			if len(out) > maxEventOutput {
				out = out[:maxEventOutput] + fmt.Sprintf("\n… (%d more bytes)", len(ev.Result.Output)-maxEventOutput)
			}
			e.emit(ctx, runID, stepID, "tool_result", map[string]any{"name": ev.Name, "id": ev.ID, "isError": ev.Result.IsError, "output": out})
			return nil, nil
		},
		AfterLLM: func(_ context.Context, ev tn.AfterLLMEvent) error {
			e.emit(ctx, runID, stepID, "llm", map[string]any{"turn": ev.Turn, "model": ev.Model, "text": assistantText(ev.Response)})
			return nil
		},
	}
}

// assistantText pulls the visible assistant text out of a raw provider response (openai or anthropic shape).
func assistantText(resp map[string]any) string {
	if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			if m, ok := c["message"].(map[string]any); ok {
				s, _ := m["content"].(string)
				return s
			}
		}
	}
	if content, ok := resp["content"].([]any); ok {
		var b strings.Builder
		for _, p := range content {
			if m, ok := p.(map[string]any); ok && m["type"] == "text" {
				b.WriteString(m["text"].(string))
			}
		}
		return b.String()
	}
	return ""
}

// saveArtifacts stores the transcript and the workspace diff for this step in S3.
func (e *Engine) saveArtifacts(ctx context.Context, runID uuid.UUID, stepID, workdir string, out agents.Outcome) {
	ctx = context.WithoutCancel(ctx)
	put := func(name, ctype string, body []byte) {
		if len(body) == 0 {
			return
		}
		key := fmt.Sprintf("runs/%s/%s/%s", runID, stepID, name)
		if err := e.blob.Put(ctx, key, bytes.NewReader(body), int64(len(body)), ctype); err != nil {
			e.emit(ctx, runID, stepID, "error", map[string]any{"text": "artifact upload failed: " + err.Error()})
			return
		}
		a := &store.Artifact{RunID: runID, StepID: stepID, Name: name, ObjectKey: key, ContentType: ctype, SizeBytes: int64(len(body))}
		if err := e.store.CreateArtifact(ctx, a); err == nil {
			e.emit(ctx, runID, stepID, "artifact", a)
		}
	}
	if b, err := json.MarshalIndent(out.Result.Messages, "", "  "); err == nil {
		put("transcript.json", "application/json", b)
	}
	if workdir != "" {
		c := exec.CommandContext(ctx, "git", "-C", workdir, "diff", "HEAD")
		c.Env = append(os.Environ(), "GIT_PAGER=cat")
		if diff, err := c.Output(); err == nil && len(bytes.TrimSpace(diff)) > 0 {
			put("workspace.diff", "text/x-diff", diff)
		}
	}
	_ = time.Now
}
