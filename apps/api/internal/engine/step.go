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

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

const maxEventOutput = 4000

// stepResult is what one step's agent produced.
type stepResult struct {
	// Output is the schema-validated submission; nil when the step parked.
	Output map[string]any
	// Pending is the human question the step suspended on, if any.
	Pending *tn.Request
	// What the step cost and said. Carried on the result rather than written
	// straight to the store, because the same function runs on a worker that
	// has no store to write to.
	Turns       int
	RawText     string
	TotalTokens int
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
	// Where it runs is the step's own `runs-on`, exactly as for a `run:` step.
	// A step placed on a worker executes THERE — the same agent, the same
	// skills, the same schema gate — against that machine's toolchain.
	if !e.servesLocally(step.RunsOn) {
		return e.runAgentRemotely(ctx, runID, def, step, prompt, data)
	}
	e.setStep(ctx, runID, step.ID, model.StepPatch{
		Status: str("running"), Prompt: str(prompt), StartedAt: now(), Error: str(""), ClearPending: true,
	})
	return e.runAgent(ctx, runID, def.Name, step, prompt, data.WorkDir, data.BaseRef)
}

// runAgent is the agent loop itself, with no run bookkeeping around it: the
// same code executes a step here and on a worker, which is the only way the
// two can be guaranteed to mean the same thing.
func (e *Engine) runAgent(ctx context.Context, runID uuid.UUID, wfName string, step *workflow.Step, prompt, workdir, baseRef string) (stepResult, error) {
	schema, err := compileSchema(wfName+"/"+step.ID, step.OutputSchema)
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
	// ask_human is an ordinary allowlisted tool name (ADR 0021). The legacy
	// step-level boolean still grants it so shipped workflows keep running, but
	// it is announced as deprecated on the run's own event stream rather than
	// in a changelog nobody reads.
	if askHumanGranted(step) {
		extra = append(extra, e.askHumanTool(step))
		if step.AskHuman && !toolNamed(step.Tools, skills.ToolAskHuman) {
			e.emit(ctx, runID, step.ID, "log", map[string]any{
				"text": "deprecated: `ask_human: true` on step " + step.ID +
					" — name `ask_human` in this step's `tools:` instead (ADR 0021)",
			})
		}
	}
	// Tools that reach back into this platform — the catalogues, validation, a
	// dry run — for a step whose job is to AUTHOR a workflow (authoring.go).
	// Granted only when the step names them, like every other tool.
	extra = append(extra, e.platformTools(step.Tools)...)

	// The whole cascade, resolved once for this step: the agent's bash tool and
	// its CLI provider must see the same environment a `run:` step would.
	env, err := e.stepEnv(ctx, runID, step, workdir)
	if err != nil {
		return stepResult{}, fmt.Errorf("step %s: %w", step.ID, err)
	}
	hooks := e.hooks(ctx, runID, step.ID, workdir, effectiveTurns(step), env)

	// The provider is resolved BEFORE the metric sink because the sink needs
	// its price: cost is computed as the tokens arrive, not reconstructed
	// afterwards from a log nobody reads back.
	prov, err := e.resolveLLMWithEnv(step, workdir, env)
	if err != nil {
		return stepResult{}, fmt.Errorf("step %s: %w", step.ID, err)
	}
	defer prov.Close()

	// Every MetricEvent still lands in the append-only event log; it is now
	// ALSO folded into the step's aggregate and written on a bounded cadence,
	// so a running step reports progress (ADR 0020). Before this, `turns` and
	// `usage` were written once, at the end: a 29-call step showed turns=0
	// until the instant it finished.
	usage := newUsageAccum(prov.Price, func(u map[string]any, turns int) {
		e.setStep(ctx, runID, step.ID, model.StepPatch{Turns: intp(turns), Usage: mustJSON(u)})
	})
	onMetric := func(m tn.MetricEvent) {
		e.emit(ctx, runID, step.ID, "metric", m)
		usage.record(m)
	}

	// A scripted transport (tests) wins over everything: it is how the wire is
	// held to zero network. Otherwise a local-process provider supplies its own.
	transport := e.transport
	if transport == nil {
		transport = prov.Transport
	}

	// Compaction is composed onto the SAME BeforeLLM seam the turn-budget
	// warning already uses, and goes first so the warning lands on the
	// compacted transcript. Off unless the step's budget names
	// `compact_at_tokens`; nil hook ⇒ chainBeforeLLM returns the warning alone.
	if compact := e.compactorHook(ctx, runID, step.ID, step, prov, transport, onMetric); compact != nil {
		hooks.BeforeLLM = chainBeforeLLM(compact, hooks.BeforeLLM)
	}

	ag, closeAgent, err := e.buildAgent(ctx, step, workdir, e.mountRoots(runID), extra, hooks, onMetric)
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

	e.emit(ctx, runID, step.ID, "log", map[string]any{
		"text": fmt.Sprintf("agent %s → %s (skills=%v tools=%v team=%d guardrails=%d)",
			step.ID, prov.Label, step.Skills, step.Tools, len(step.Team), len(step.Guardrails)),
	})

	res, rt := ag.Run(agents.Options{
		LLM:       prov.LLM,
		Transport: transport,
	}, prompt)

	// TotalTokens is PARENT-ONLY; the sub-agent tree's spend lives on the
	// runtime (spikes/01), so billing must read the tree.
	totalTokens := res.TotalTokens
	if rt != nil {
		if tree := rt.UsageTokens(rt.Root); tree > totalTokens {
			totalTokens = tree
		}
	}
	// The final write. `totalTokens` keeps its old meaning and its old place —
	// the accumulator only counts what the LLM reported per call, so where the
	// runtime's whole-tree rollup is larger (a sub-agent team, spikes/01) that
	// larger number wins and the aggregate says so.
	final, turns := usage.final()
	if totalTokens > final["totalTokens"].(int) {
		final["totalTokens"] = totalTokens
		final["treeTokens"] = totalTokens
	}
	if res.Turns > turns {
		turns = res.Turns
	}
	e.setStep(ctx, runID, step.ID, model.StepPatch{
		Turns: intp(turns), RawText: str(res.Text), Usage: mustJSON(final),
	})
	e.saveArtifacts(ctx, runID, step.ID, workdir, baseRef, res)
	spent := stepResult{Turns: res.Turns, RawText: res.Text, TotalTokens: totalTokens}

	switch {
	case res.Status == "pending" && res.Pending != nil:
		// A durable halt: the Request is plain data, so the question outlives
		// this process and the answer may arrive hours later.
		spent.Pending = res.Pending
		return spent, nil
	case submitted != nil:
		spent.Output = submitted
		return spent, nil
	case res.IsError || res.Status == "error":
		return stepResult{}, fmt.Errorf("step %s failed: %s", step.ID, scrub(res.Text))
	default:
		// The runtime reports no structured limit, so the reason is stated in
		// full rather than guessed at (spikes/15).
		return stepResult{}, fmt.Errorf("step %s stopped as %q without a valid result: %s",
			step.ID, res.Status, scrub(trim(res.Text, 500)))
	}
}

// askHumanGranted reports whether this step may interrupt a person: the tool
// named in `tools:` (the way it should be), or the deprecated boolean.
func askHumanGranted(step *workflow.Step) bool {
	return step.AskHuman || toolNamed(step.Tools, skills.ToolAskHuman)
}

func toolNamed(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
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

// hooks stream the agent's activity into the run event log, pin bash to the
// run's workspace, and warn the agent when it is running out of turns.
//
// turns is the step's turn ceiling (0 ⇒ none), needed for that last part.
func (e *Engine) hooks(ctx context.Context, runID uuid.UUID, stepID, workdir string, turns int, env map[string]string) *tn.Hooks {
	// The builtin `bash` tool has no env argument and inherits this process's
	// environment, so a step's own env goes in front of the command. A
	// reference is left AS a reference — GH_TOKEN="${GITHUB_PAT}" — which the
	// child shell expands from what it already inherited. The value is
	// therefore never rendered, never logged, and never part of the tool call
	// the run records; only the name is, which is what the file says anyway.
	envPrefix := workflow.ShellPrefix(env)
	return &tn.Hooks{
		// The soul states the budget once, at turn 0. That is necessary and it is
		// not sufficient: `triage/classify` was told it had 15 turns, spent all 15
		// on a genuinely useful investigation, never called submit_output, and the
		// run failed with nothing recorded — the same shape as the earlier
		// `code-review` failure. An agent cannot count its own turns, so the
		// remaining count is put in front of it near the ceiling, every turn,
		// until it submits.
		BeforeLLM: func(_ context.Context, ev tn.BeforeLLMEvent) (*tn.LLMOverride, error) {
			left := turns - ev.Turn
			if turns <= 0 || left > lastTurnsWarning(turns) || left < 0 {
				return nil, nil
			}
			e.emit(ctx, runID, stepID, "log", map[string]any{
				"text": fmt.Sprintf("turn %d/%d — warning the agent it has %d left", ev.Turn, turns, left),
			})
			msgs := append(append([]any{}, ev.Messages...), map[string]any{
				"role": "user",
				"content": fmt.Sprintf("BUDGET: %d of your %d turns remain. Stop gathering and call "+
					"submit_output now with what you have. State plainly in the output what is "+
					"uncertain or unfinished — a submitted partial result is recorded, and an "+
					"unsubmitted complete one is lost entirely.", left, turns),
			})
			return &tn.LLMOverride{Messages: msgs}, nil
		},
		BeforeTool: func(_ context.Context, ev tn.BeforeToolEvent) (*tn.ToolOverride, error) {
			var ov *tn.ToolOverride
			if ev.Name == "bash" && (workdir != "" || envPrefix != "") {
				args := map[string]any{}
				for k, v := range ev.Args {
					args[k] = v
				}
				if wd, _ := args["workdir"].(string); wd == "" && workdir != "" {
					args["workdir"] = workdir
				}
				if cmd, _ := args["command"].(string); cmd != "" && envPrefix != "" {
					args["command"] = envPrefix + cmd
				}
				ov = &tn.ToolOverride{Args: args}
				ev.Args = args
			} else if args := pinPaths(ev.Name, ev.Args, workdir); args != nil {
				// A relative path belongs to the WORKSPACE, not to this process's
				// working directory, which is where the builtins would otherwise
				// resolve it (paths.go).
				ov = &tn.ToolOverride{Args: args}
				ev.Args = args
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

// lastTurnsWarning is how many turns before the ceiling the warning starts:
// a fifth of the budget, at least 2 and at most 5. Proportional because a
// 60-turn step needs more notice than an 8-turn one; capped because a long
// step should not spend a quarter of itself being nagged.
func lastTurnsWarning(turns int) int {
	n := turns / 5
	if n < 2 {
		n = 2
	}
	if n > 5 {
		n = 5
	}
	return n
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
	if e.blob == nil || e.store == nil {
		return // a worker has neither; its evidence travels back on the result
	}
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
		a := &model.Artifact{RunID: runID, StepID: stepID, Name: name, ObjectKey: key, ContentType: ctype, SizeBytes: int64(len(body))}
		if err := e.store.CreateArtifact(ctx, a); err == nil {
			e.emit(ctx, runID, stepID, "artifact", a)
		}
	}
	if res.Text != "" {
		put("final.txt", "text/plain", []byte(res.Text))
	}
	if diff := workspaceDiff(ctx, workdir, baseRef); len(diff) > 0 {
		put("workspace.diff", "text/x-diff", diff)
	}
}

// workspaceDiff is what the step changed, as evidence. On a worker the work
// happens on that machine's disk, so this is how it reaches the run at all.
func workspaceDiff(ctx context.Context, workdir, baseRef string) []byte {
	if workdir == "" {
		return nil
	}
	from := baseRef
	if from == "" {
		from = "HEAD"
	}
	c := exec.CommandContext(context.WithoutCancel(ctx), "git", "-C", workdir, "diff", from)
	c.Env = append(os.Environ(), "GIT_PAGER=cat")
	diff, err := c.Output()
	if err != nil || len(bytes.TrimSpace(diff)) == 0 {
		return nil
	}
	return diff
}
