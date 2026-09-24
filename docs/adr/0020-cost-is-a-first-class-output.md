# ADR 0020 — Cost is a first-class output

- **Status:** proposed
- **Date:** 2026-09-24

## Context

`code-review` ran against this repo on 2026-09-24, `main~3..main`, sonnet-4.5
via OpenRouter. It completed: `survey` 4 turns, `review` 27 turns, both with
validated typed output. 31 LLM calls, 30 tool calls.

It also spent **1,424,154 prompt tokens against 6,541 completion tokens** —
**$4.37** at $3/$15 per million.

That figure was computed **by hand**, from `metric` events the platform already
emits and then discards. The platform reported no cost anywhere: not on the run,
not on the step, not in the CLI. The data arrives per call —
`MetricEvent` carries `PromptTokens`, `CompletionTokens`, `Model`, `Ms`, `Tool`
— and `engine/step.go:106` forwards it straight into the event log:

```go
onMetric := func(m tn.MetricEvent) { e.emit(ctx, runID, step.ID, "metric", m) }
```

Nothing reads it back. The ROADMAP already records this ("**No metrics
endpoint.** … nothing aggregates it"), filed as a missing endpoint. It is not a
missing endpoint. It is a missing column.

What `step_runs` keeps today is one blob written **once, at step end**
(`step.go:155`): `usage = {"totalTokens": N}`, alongside `turns`. Hence the
other defect from the same run — `review` reported `turns=0` through 29 LLM
calls and jumped to `27` the instant it finished. A running step reports no
progress because nothing about a running step is ever written.

And the 218:1 prompt/completion ratio is the 117 KB diff being re-sent every
turn. `agents.Compactor` — a `BeforeLLM` hook, present in the pinned toolnexus —
is unused.

## Decision

**Cost and usage are aggregated and persisted per step and per run, and written
as they accrue.**

1. **Keep what already arrives.** `MetricEvent` is folded into a per-step
   accumulator and persisted: LLM calls, tool calls, prompt and completion
   tokens, wall time, per model. Nothing new is measured. The event log stays the
   append-only truth; the aggregate is the queryable roll-up, and the run's
   totals are the sum of its steps'.
2. **A running step reports progress.** Turns, tokens and cost are written on a
   bounded cadence while the step runs, not at its end. **This is a data-model
   decision, not a UI one** — the "what is this agent doing right now" screen
   cannot be built at any quality until the store records a running step, and no
   front end can render a column that is only written after the answer is known.
3. **Price is a property of a registry provider entry** (per-million in, per-
   million out), because ADR 0011 already makes the registry the one place a step
   names a backend. Cost is therefore computed for *every* backend — including a
   `cli` provider, where the correct number is **$0.00**. That zero is a feature:
   it is the one number a local-first runtime can show that a hosted product
   structurally cannot, and it makes the portability claim of the pivot legible
   in currency.
4. **Compaction is reclassified.** The ROADMAP calls it "the single biggest gap"
   as a capability — a long step dying at the context limit. The evidence makes
   it a **money** problem first: a 218:1 ratio is the bill, on a run that never
   came near the limit.

**Adopt, never invent:** the vocabulary is OTEL/Prometheus
(`gen_ai.*` attributes, a counter per token kind, a histogram for duration), not
names of our own. An export path is an exporter over the same aggregate, not a
second measurement.

## The three-scale test

| scale | what this must answer |
|---|---|
| **one person** | "what did that cost me", on the run page, next to the work — and `$0.00` when they ran it on their own CLI or a local model. |
| **a small org** | spend per project and per workflow. Which workflow is expensive, and which step inside it. |
| **an enterprise** | chargeback/showback per project, budget enforcement, and an OTEL/Prometheus export so it lands in the monitoring they already run rather than in a dashboard of ours. |

Projects are already in the schema (migration `000004_run_project`), so the org
and enterprise scales need no new dimension — only that the roll-up carries the
project id it already has.

## UX

The pivot's rule holds: **extremely simple, the run is the hero, drawn as time.**
Cost sits on the run, beside the step that spent it. There is no billing page,
no separate analytics section. A number you have to navigate to is a number
nobody reads.

## Consequences

- `step_runs.usage` stops being a write-once blob and becomes a live aggregate;
  the write cadence is now a thing that can be got wrong (too chatty, or too
  coarse to look live).
- Persisting during a run means a step that crashes still leaves its spend
  behind. Today it leaves nothing.
- A missing or stale price yields tokens without a cost. That must render as
  *unknown*, never as `$0.00` — the local zero has to stay trustworthy.

## Not decided here

- **The pricing table's source of truth**, and how it stays current. Models
  change price; a hard-coded table is wrong within a month.
- **Currency.** One, or per-provider, or stored in the provider's and converted.
- **Whether a budget hard-stops a run or only warns.** A hard stop kills work
  mid-flight with a half-written worktree; a warning nobody reads is not
  enforcement. Named as open.
