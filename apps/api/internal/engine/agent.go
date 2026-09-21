package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// buildAgent assembles the toolnexus agent for one step: its identity, its
// scoped toolkit, its sub-agent team, its policy and its budget.
//
// The returned closer releases every toolkit built here (the step's and each
// team member's).
func (e *Engine) buildAgent(ctx context.Context, step *workflow.Step, workdir string, extra []tn.Tool, hooks *tn.Hooks, onMetric func(tn.MetricEvent)) (*agents.Agent, func(), error) {
	var toolkits []*tn.Toolkit
	closer := func() {
		for _, tk := range toolkits {
			tk.Close()
		}
	}

	team, teamTks, err := e.buildTeam(ctx, step, workdir, hooks, onMetric)
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
		Soul:       stepSoul(step, workdir, tk),
		Tools:      tk.Tools(),
		Team:       team,
		Model:      step.Model,
		Budget:     toBudget(step.Budget, step.MaxTurns),
		Guardrails: withContainment(workdir, step.Guardrails),
		Hooks:      hooks,
		OnMetric:   onMetric,
	})
	return ag, closer, nil
}

func (e *Engine) buildTeam(ctx context.Context, step *workflow.Step, workdir string, hooks *tn.Hooks, onMetric func(tn.MetricEvent)) ([]*agents.Agent, []*tn.Toolkit, error) {
	var team []*agents.Agent
	var tks []*tn.Toolkit
	for _, m := range step.Team {
		tk, err := e.buildToolkit(ctx, step.ID+"/"+m.ID, m.Skills, m.Tools, nil)
		if err != nil {
			return nil, tks, err
		}
		tks = append(tks, tk)
		team = append(team, agents.New(m.ID, agents.Spec{
			Does:  m.Does,
			Soul:  withSkills(m.Soul, tk),
			Tools: tk.Tools(),
			Model: m.Model,
			// a sub-agent is contained exactly like its parent
			Guardrails: withContainment(workdir, nil),
			Budget:     toBudget(m.Budget, 0),
			Hooks:      hooks,
			OnMetric:   onMetric,
		}))
	}
	return team, tks, nil
}

// stepSoul composes what the step's agent is told: its own identity, where it
// works, how its result is recorded, and HOW MANY TURNS IT HAS.
//
// The turn budget is not decoration. A review step was given 40 turns, spent
// all of them reading a 777-line diff (27 bash, 11 read) and never submitted,
// so the run failed with nothing recorded although the work was nearly done.
// The agent had no way to know it was near a limit: it explored as if
// unbounded and discovered the ceiling by being cut off.
func stepSoul(step *workflow.Step, workdir string, tk *tn.Toolkit) string {
	var b strings.Builder
	if step.Soul != "" {
		b.WriteString(step.Soul)
		b.WriteString("\n\n")
	}
	if workdir != "" {
		fmt.Fprintf(&b, "You are working in %s. Run shell commands there and use paths relative to it; "+
			"do not cd outside it.\n", workdir)
	}
	b.WriteString("When your work is complete, call `submit_output` exactly once with JSON matching its " +
		"schema. That call is the ONLY way your result is recorded.\n")
	if turns := effectiveTurns(step); turns > 0 {
		fmt.Fprintf(&b, "You have at most %d turns for this step and cannot ask for more. Budget them: "+
			"gather what you need, then submit. If you are running short, submit your best result with "+
			"what you have and say plainly what is uncertain — a submitted partial answer is recorded, "+
			"an unsubmitted perfect one is lost.\n", turns)
	}
	return withSkills(b.String(), tk)
}

// effectiveTurns is the ceiling the step will actually get: the tighter of its
// own cap and its budget, which is the number the agent needs to hear.
func effectiveTurns(step *workflow.Step) int {
	n := step.MaxTurns
	if step.Budget != nil && step.Budget.MaxTurns > 0 && (n == 0 || step.Budget.MaxTurns < n) {
		n = step.Budget.MaxTurns
	}
	return n
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

// withContainment puts the workspace guardrail AHEAD of the step's own rules.
// First deny wins, so a YAML rule can never widen it.
func withContainment(workdir string, rules []workflow.Guardrail) []agents.Guardrail {
	out := []agents.Guardrail{containmentGuardrail(workdir)}
	return append(out, compileGuardrails(rules)...)
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
