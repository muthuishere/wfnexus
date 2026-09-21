package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

// buildAgent assembles the toolnexus agent for one step: its identity, its
// scoped toolkit, its sub-agent team, its policy and its budget.
//
// The returned closer releases every toolkit built here (the step's and each
// team member's).
func (e *Engine) buildAgent(ctx context.Context, step *workflow.Step, extra []tn.Tool, hooks *tn.Hooks, onMetric func(tn.MetricEvent)) (*agents.Agent, func(), error) {
	var toolkits []*tn.Toolkit
	closer := func() {
		for _, tk := range toolkits {
			tk.Close()
		}
	}

	team, teamTks, err := e.buildTeam(ctx, step, hooks, onMetric)
	if err != nil {
		closer()
		return nil, nil, err
	}
	toolkits = append(toolkits, teamTks...)

	tk, err := e.buildToolkit(ctx, step.ID, step.Skills, step.Tools, step.MCP)
	if err != nil {
		closer()
		return nil, nil, err
	}
	toolkits = append(toolkits, tk)
	tk.Register(extra...)

	ag := agents.New(step.ID, agents.Spec{
		Does: step.Description,
		// The runtime sets SystemPrompt = Soul, so the skills catalogue has to be
		// folded in here; nothing else advertises it on this path.
		Soul:       withSkills(step.Soul, tk),
		Tools:      tk.Tools(),
		Team:       team,
		Model:      step.Model,
		Budget:     toBudget(step.Budget, step.MaxTurns),
		Guardrails: compileGuardrails(step.Guardrails),
		Hooks:      hooks,
		OnMetric:   onMetric,
	})
	return ag, closer, nil
}

func (e *Engine) buildTeam(ctx context.Context, step *workflow.Step, hooks *tn.Hooks, onMetric func(tn.MetricEvent)) ([]*agents.Agent, []*tn.Toolkit, error) {
	var team []*agents.Agent
	var tks []*tn.Toolkit
	for _, m := range step.Team {
		tk, err := e.buildToolkit(ctx, step.ID+"/"+m.ID, m.Skills, m.Tools, nil)
		if err != nil {
			return nil, tks, err
		}
		tks = append(tks, tk)
		team = append(team, agents.New(m.ID, agents.Spec{
			Does:     m.Does,
			Soul:     withSkills(m.Soul, tk),
			Tools:    tk.Tools(),
			Model:    m.Model,
			Budget:   toBudget(m.Budget, 0),
			Hooks:    hooks,
			OnMetric: onMetric,
		}))
	}
	return team, tks, nil
}

// withSkills appends the toolkit's skills catalogue to an agent's soul.
func withSkills(soul string, tk *tn.Toolkit) string {
	prompt := tk.SkillsPrompt()
	switch {
	case prompt == "":
		return soul
	case soul == "":
		return prompt
	}
	return soul + "\n\n" + prompt
}

func toBudget(b *workflow.Budget, maxTurns int) *agents.Budget {
	if b == nil {
		if maxTurns <= 0 {
			return nil
		}
		return &agents.Budget{MaxTurns: maxTurns}
	}
	out := &agents.Budget{
		MaxTurns:      b.MaxTurns,
		MaxTokens:     b.MaxTokens,
		MaxToolCalls:  b.MaxToolCalls,
		MaxChildren:   b.MaxChildren,
		MaxConcurrent: b.MaxConcurrent,
		MaxDepth:      b.MaxDepth,
	}
	if out.MaxTurns == 0 {
		out.MaxTurns = maxTurns
	}
	if b.MaxWallSec > 0 {
		out.MaxWall = secs(b.MaxWallSec)
	}
	return out
}

// compileGuardrails turns the YAML policy rules into toolnexus guardrails.
// First deny wins — a later rule can never widen an earlier denial — and the
// model receives the reason as the tool result, so the reason is the message.
func compileGuardrails(rules []workflow.Guardrail) []agents.Guardrail {
	var out []agents.Guardrail
	for _, r := range rules {
		rule := r
		out = append(out, func(ev tn.BeforeToolEvent) string {
			if rule.Deny != "*" && rule.Deny != ev.Name {
				return ""
			}
			if len(rule.ArgsContain) == 0 {
				return rule.Reason
			}
			blob := strings.ToLower(argText(ev.Args))
			for _, needle := range rule.ArgsContain {
				if strings.Contains(blob, strings.ToLower(needle)) {
					return rule.Reason
				}
			}
			return ""
		})
	}
	return out
}

// argText flattens a tool call's arguments to one searchable string.
func argText(args map[string]any) string {
	var b strings.Builder
	for _, v := range args {
		fmt.Fprintf(&b, "%v\n", v)
	}
	return b.String()
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }
