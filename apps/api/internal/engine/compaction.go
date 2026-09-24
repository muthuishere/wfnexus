package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Context compaction, wired through toolnexus's own agents.Compactor (v0.19.0,
// agents/compaction.go) and its shipped BeforeLLM seam. Nothing is reimplemented
// here: ADR 0001 says compaction is toolnexus's, and it is. This file only
// decides WHEN it runs, what the summary is asked to preserve, and makes the
// rewrite visible on the run.
//
// It is OFF unless the step asks for it (`budget: { compact_at_tokens: N }`).
// Off by default is the safer choice: compaction rewrites the transcript the
// typed contract is carried in, so it is opt-in per step rather than a silent
// change to every shipped workflow. A step that does not name it is
// byte-identical to before.
//
// It also never violates ADR 0005: the transcript is rewritten in memory for
// the NEXT model call inside the same step. No toolnexus resume, no durability
// boundary moved — the step is still the unit that either submitted or did not.

// compactSummarySoul is what the summarizer is told. The step's instructions and
// its output contract are the one thing a summary may not lose: a compaction
// that drops them turns a correct step into a step that never submits.
const compactSummarySoul = `You compress an agent's working transcript so it can keep going under a context limit.

Write a dense summary of the transcript below. It REPLACES those messages, so
anything you leave out is gone. You MUST preserve, verbatim where they are short:

1. The task the agent was given, including every constraint and every literal
   value in it (paths, branch names, file names).
2. The exact shape of the output the agent still has to submit via submit_output,
   including required field names and any enum values it must choose from.
3. Findings, measurements, file:line references and command results the agent has
   already established — these are the work and must not be re-derived.
4. What the agent has already tried that did not work.

Do not editorialise, do not add advice, and do not say the transcript was
summarized. Output the summary only.`

// compactorHook builds the BeforeLLM compaction hook for a step, or nil when the
// step did not ask for compaction.
//
// The summarizer runs on the SAME provider as the step: one extra LLM call at
// the moment of compaction, on a model the step is already paying for and
// already authorized to use. Its usage flows into the step's accumulator through
// onMetric like every other call, so the run's cost stays truthful.
func (e *Engine) compactorHook(ctx context.Context, runID uuid.UUID, stepID string, step *workflow.Step,
	prov resolved, transport http.RoundTripper, onMetric func(tn.MetricEvent)) func(context.Context, tn.BeforeLLMEvent) (*tn.LLMOverride, error) {

	max := compactAtTokens(step)
	if max <= 0 {
		return nil
	}
	summarize := e.summarizer(ctx, runID, stepID, prov, transport, onMetric)
	inner := agents.Compactor(agents.CompactorOptions{
		MaxTokens: max,
		Summarize: summarize,
	})
	return func(hctx context.Context, ev tn.BeforeLLMEvent) (*tn.LLMOverride, error) {
		before := agents.EstimateTokens(ev.Messages)
		ov, err := inner(hctx, ev)
		if err != nil {
			// A failed summary must not kill a step that was otherwise fine: the
			// turn proceeds uncompacted and the run says so. The next turn tries
			// again, and if the context really is full the provider's own error
			// is the honest one to fail on.
			e.emit(ctx, runID, stepID, "log", map[string]any{
				"text": fmt.Sprintf("compaction failed at turn %d (~%d tokens), continuing uncompacted: %v",
					ev.Turn, before, err),
			})
			return nil, nil
		}
		if ov == nil || ov.Messages == nil {
			return ov, nil
		}
		// A silent context rewrite is undebuggable later; this is the record that
		// it happened, when, and how much it removed.
		e.emit(ctx, runID, stepID, "log", map[string]any{
			"text": fmt.Sprintf("compacted context at turn %d: %d messages ~%d tokens → %d messages ~%d tokens (threshold %d)",
				ev.Turn, len(ev.Messages), before, len(ov.Messages), agents.EstimateTokens(ov.Messages), max),
		})
		return ov, nil
	}
}

// summarizer is the Summarize function agents.Compactor requires. toolnexus makes
// no model call on the host's behalf, so the host supplies one; this is a plain
// single-turn completion with no toolkit.
func (e *Engine) summarizer(ctx context.Context, runID uuid.UUID, stepID string,
	prov resolved, transport http.RoundTripper, onMetric func(tn.MetricEvent)) func([]any) (string, error) {

	return func(older []any) (string, error) {
		blob, err := json.Marshal(older)
		if err != nil {
			return "", fmt.Errorf("compaction: marshal transcript: %w", err)
		}
		opts := tn.ClientOptions{
			SystemPrompt: compactSummarySoul,
			MaxTurns:     1,
			OnMetric:     onMetric,
		}
		if prov.LLM != nil {
			opts.BaseURL = prov.LLM.BaseURL
			opts.Style = prov.LLM.Style
			opts.Model = prov.LLM.Model
			opts.APIKey = prov.LLM.APIKey
		}
		if transport != nil {
			opts.HTTPClient = &http.Client{Transport: transport}
		}
		res, err := tn.CreateClient(opts).Run(ctx, "Transcript to summarize:\n"+string(blob), nil)
		if err != nil {
			return "", err
		}
		if res.Text == "" {
			return "", fmt.Errorf("compaction: summarizer returned no text")
		}
		return res.Text, nil
	}
}

// compactAtTokens is the step's compaction threshold: `budget.compact_at_tokens`.
// 0 (the default) ⇒ compaction is off for this step.
func compactAtTokens(step *workflow.Step) int {
	if step.Budget == nil {
		return 0
	}
	return int(step.Budget.CompactAtTokens)
}

// chainBeforeLLM runs BeforeLLM hooks in order, threading each override's
// messages into the next one's event, so the last hook sees what the model will
// actually receive. Compaction goes FIRST: the turn-budget warning must land on
// the compacted transcript, not be summarized away moments after being added.
func chainBeforeLLM(hooks ...func(context.Context, tn.BeforeLLMEvent) (*tn.LLMOverride, error)) func(context.Context, tn.BeforeLLMEvent) (*tn.LLMOverride, error) {
	var live []func(context.Context, tn.BeforeLLMEvent) (*tn.LLMOverride, error)
	for _, h := range hooks {
		if h != nil {
			live = append(live, h)
		}
	}
	if len(live) == 0 {
		return nil
	}
	if len(live) == 1 {
		return live[0]
	}
	return func(ctx context.Context, ev tn.BeforeLLMEvent) (*tn.LLMOverride, error) {
		var out *tn.LLMOverride
		for _, h := range live {
			ov, err := h(ctx, ev)
			if err != nil {
				return nil, err
			}
			if ov == nil {
				continue
			}
			if ov.Messages != nil {
				ev.Messages = ov.Messages
			}
			out = merged(out, ov)
		}
		return out, nil
	}
}

// merged folds a later override onto an earlier one; a nil field leaves the
// earlier value in place, which is the semantics tn.LLMOverride already documents.
func merged(a, b *tn.LLMOverride) *tn.LLMOverride {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := *a
	if b.Messages != nil {
		out.Messages = b.Messages
	}
	if b.Tools != nil {
		out.Tools = b.Tools
	}
	return &out
}
