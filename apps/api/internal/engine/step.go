package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/skills"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/store"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

const maxEventOutput = 4000

// stepResult is what one step's agent produced.
type stepResult struct {
	// Output is the schema-validated submission; nil when the step parked.
	Output map[string]any
	// Pending is the human question the step suspended on, if any.
	Pending *tn.Request
}

// executeStep runs ONE workflow step as a toolnexus agent and returns its
// schema-validated output.
//
// The step is a whole agent, not a prompt: its own soul, its scoped skills and
// tools, an optional sub-agent team it may delegate to, policy guardrails, and
// a budget. It runs on the AGENTS RUNTIME rather than the plain loop, because
// guardrails and teams exist only there (spikes/02, spikes/01).
//
// The output contract is enforced inside the loop: the step's JSON schema is
// the input schema of a native `submit_output` tool; a rejected submission
// returns the validation errors as the tool result so the model self-corrects,
// and a Completion gate refuses `done` until one was accepted.
func (e *Engine) executeStep(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, data workflow.TemplateData) (stepResult, error) {
	prompt, err := workflow.Render(step.Prompt, data)
	if err != nil {
		return stepResult{}, fmt.Errorf("render prompt: %w", err)
	}
	e.setStep(ctx, runID, step.ID, store.StepPatch{
		Status: str("running"), Prompt: str(prompt), StartedAt: now(), Error: str(""), ClearPending: true,
	})

	schema, err := compileSchema(def.Name+"/"+step.ID, step.OutputSchema)
	if err != nil {
		return stepResult{}, fmt.Errorf("output_schema: %w", err)
	}

	// The accepted submission is captured here rather than read back off the
	// result: on success the final turn is a tool call, so Outcome.Text is
	// empty (spikes/06).
	var submitted map[string]any
	submit := tn.NativeTool("submit_output",
		"Submit the FINAL result of this step as JSON matching the required schema. Call exactly once when the work is complete. If it returns validation errors, fix them and call again.",
		step.OutputSchema,
		func(_ context.Context, args map[string]any) (string, error) {
			if err := validateJSON(schema, args); err != nil {
				return "", err
			}
			submitted = args
			return "accepted", nil
		})

	extra := []tn.Tool{submit}
	if step.AskHuman {
		extra = append(extra, e.askHumanTool(step))
	}

	hooks := e.hooks(ctx, runID, step.ID, data.WorkDir)
	onMetric := func(m tn.MetricEvent) { e.emit(ctx, runID, step.ID, "metric", m) }

	ag, closeAgent, err := e.buildAgent(ctx, step, extra, hooks, onMetric)
	if err != nil {
		return stepResult{}, err
	}
	defer closeAgent()

	// The completion gate travels WITH the agent, so a delegated child inherits
	// it — which a host-side retry loop cannot do.
	ag.Spec.Completion = &agents.Completion{
		MaxAttempts: step.MaxAttempts,
		Verify: func(tn.RunResult) (bool, string) {
			if submitted != nil {
				return true, ""
			}
			return false, "you have not called submit_output with an accepted result yet; finish the work and call submit_output with JSON matching its schema"
		},
	}

	model := step.Model
	if model == "" {
		model = e.cfg.Model
	}
	e.emit(ctx, runID, step.ID, "log", map[string]any{
		"text": fmt.Sprintf("agent %s → %s (skills=%v tools=%v team=%d guardrails=%d)",
			step.ID, model, step.Skills, step.Tools, len(step.Team), len(step.Guardrails)),
	})

	res, rt := ag.Run(agents.Options{
		LLM: &agents.LLMOptions{
			BaseURL: e.cfg.LLMBaseURL,
			Style:   tn.ClientStyle(e.cfg.LLMStyle),
			Model:   model,
			APIKey:  os.Getenv(e.cfg.LLMAPIKeyEnv),
		},
		Transport: e.transport,
	}, prompt)

	// TotalTokens is PARENT-ONLY; the sub-agent tree's spend lives on the
	// runtime (spikes/01), so billing must read the tree.
	totalTokens := res.TotalTokens
	if rt != nil {
		if tree := rt.UsageTokens(rt.Root); tree > totalTokens {
			totalTokens = tree
		}
	}
	e.setStep(ctx, runID, step.ID, store.StepPatch{
		Turns: intp(res.Turns), RawText: str(res.Text),
		Usage: mustJSON(map[string]any{"totalTokens": totalTokens}),
	})
	e.saveArtifacts(ctx, runID, step.ID, data.WorkDir, data.BaseRef, res)

	switch {
	case res.Status == "pending" && res.Pending != nil:
		// A durable halt: the Request is plain data, so the question outlives
		// this process and the answer may arrive hours later.
		return stepResult{Pending: res.Pending}, nil
	case submitted != nil:
		return stepResult{Output: submitted}, nil
	case res.IsError || res.Status == "error":
		return stepResult{}, fmt.Errorf("step %s failed: %s", step.ID, scrub(res.Text))
	default:
		// The runtime reports no structured limit, so the reason is stated in
		// full rather than guessed at (spikes/15).
		return stepResult{}, fmt.Errorf("step %s stopped as %q without a valid result: %s",
			step.ID, res.Status, scrub(trim(res.Text, 500)))
	}
}

// askHumanTool lets the step's agent stop and ask. It suspends with a Request;
// with no WaitFor configured the run halts durably and the engine parks the run
// in needs_input. NativeTool cannot express this — its function never sees the
// ToolContext — so it is a raw Tool (spikes/03).
func (e *Engine) askHumanTool(step *workflow.Step) tn.Tool {
	return tn.Tool{
		Name:        "ask_human",
		Description: "Ask the human operator a question and wait for their answer. Use only when you genuinely cannot proceed; the run pauses until they reply.",
		Source:      tn.SourceCustom,
		InputSchema: tn.JSONSchema{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "What you need to know, in one sentence."},
				"why":      map[string]any{"type": "string", "description": "Why you cannot proceed without it."},
			},
			"required":             []any{"question"},
			"additionalProperties": false,
		},
		Execute: func(args map[string]any, tc *tn.ToolContext) (tn.ToolResult, error) {
			if tc != nil && tc.Answer != nil {
				// the post-Answer retry: only the pinned output key is read back
				out, _ := tc.Answer.Data[tn.RelayOutputKey].(string)
				if out == "" {
					out = "(the operator gave no answer)"
				}
				return tn.ToolResult{Output: out}, nil
			}
			q, _ := args["question"].(string)
			why, _ := args["why"].(string)
			return tn.Pending(tn.Request{
				ID:     uuid.NewString(),
				Kind:   "input",
				Prompt: q,
				Data:   map[string]any{"why": why, "step": step.ID},
			}), nil
		},
	}
}

// buildToolkit assembles a scoped toolkit: skills allowlist, builtin allowlist,
// MCP server allowlist. Scoping is the security model — an agent sees exactly
// what its YAML lists and nothing else.
func (e *Engine) buildToolkit(ctx context.Context, label string, skillNames, tools, mcp []string) (*tn.Toolkit, error) {
	opts := tn.Options{}
	if len(skillNames) > 0 {
		opts.SkillsDir = e.skills.RootsFor(skillNames)
		if len(opts.SkillsDir) == 0 {
			return nil, fmt.Errorf("%s: none of the skills %v are in the registry", label, skillNames)
		}
		opts.SkillsFilter = map[string]bool{}
		for _, s := range skillNames {
			opts.SkillsFilter[s] = true
		}
	}
	opts.Builtins = skills.BuiltinAllowlist(tools)
	if len(mcp) > 0 {
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
		for _, name := range mcp {
			s, ok := servers[name]
			if !ok {
				return nil, fmt.Errorf("%s: mcp server %q not in %s", label, name, e.cfg.McpConfig)
			}
			keep[name] = s
		}
		opts.McpConfig = map[string]any{"mcpServers": keep}
	}
	return tn.CreateToolkit(ctx, opts)
}

// hooks stream the agent's activity into the run event log and pin bash to the
// run's workspace.
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
			payload := map[string]any{"name": ev.Name, "id": ev.ID, "turn": ev.Turn, "args": ev.Args}
			if ev.Name == "task" {
				payload["agent"], _ = ev.Args["agent"].(string) // delegation to a team member
			}
			e.emit(ctx, runID, stepID, "tool_call", payload)
			return ov, nil
		},
		// A guardrail denial short-circuits the call, so AfterTool does not fire
		// for it (spikes/02) — the denial is visible in the model's next turn.
		AfterTool: func(_ context.Context, ev tn.AfterToolEvent) (*tn.ToolOverride, error) {
			e.emit(ctx, runID, stepID, "tool_result", map[string]any{
				"name": ev.Name, "id": ev.ID, "isError": ev.Result.IsError,
				"output": trim(scrub(ev.Result.Output), maxEventOutput),
			})
			return nil, nil
		},
		AfterLLM: func(_ context.Context, ev tn.AfterLLMEvent) error {
			e.emit(ctx, runID, stepID, "llm", map[string]any{
				"turn": ev.Turn, "model": ev.Model, "text": assistantText(ev.Response),
			})
			return nil
		},
	}
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n… (%d more bytes)", len(s)-n)
}

// assistantText pulls the visible assistant text out of a raw provider response.
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
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

// saveArtifacts stores the step's transcript and the workspace diff in S3.
func (e *Engine) saveArtifacts(ctx context.Context, runID uuid.UUID, stepID, workdir, baseRef string, res agents.TaskResult) {
	ctx = context.WithoutCancel(ctx)
	put := func(name, ctype string, body []byte) {
		if len(body) == 0 {
			return
		}
		key := fmt.Sprintf("runs/%s/%s/%s", runID, stepID, name)
		if err := e.blob.Put(ctx, key, bytes.NewReader(body), int64(len(body)), ctype); err != nil {
			e.emit(ctx, runID, stepID, "error", map[string]any{"text": "artifact upload failed: " + scrub(err.Error())})
			return
		}
		a := &store.Artifact{RunID: runID, StepID: stepID, Name: name, ObjectKey: key, ContentType: ctype, SizeBytes: int64(len(body))}
		if err := e.store.CreateArtifact(ctx, a); err == nil {
			e.emit(ctx, runID, stepID, "artifact", a)
		}
	}
	if res.Text != "" {
		put("final.txt", "text/plain", []byte(res.Text))
	}
	if workdir != "" {
		from := baseRef
		if from == "" {
			from = "HEAD"
		}
		c := exec.CommandContext(ctx, "git", "-C", workdir, "diff", from)
		c.Env = append(os.Environ(), "GIT_PAGER=cat")
		if diff, err := c.Output(); err == nil && len(bytes.TrimSpace(diff)) > 0 {
			put("workspace.diff", "text/x-diff", diff)
		}
	}
}
