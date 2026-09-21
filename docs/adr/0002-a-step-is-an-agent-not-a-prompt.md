# ADR 0002 — A step is a whole agent, not a prompt

- **Status:** accepted
- **Date:** 2026-09-21

## Context

The obvious shape for "AI workflow" is a chain of prompts: each node renders a
template, calls a model, and passes the text along. Every visual builder on the
market is some version of that.

It does not survive contact with a coding task. Fixing a bug needs many turns,
tool calls, a test run, a retry when the test fails — a loop, not a call.

## Decision

A workflow step is an entire agent with its own harness:

```
step = soul + prompt + skills[] + tools[] + mcp[] + team[]
     + guardrails[] + budget{} + decide{} + output_schema + gates[]
```

The step runs until its agent submits an output that validates, or a limit stops
it loudly. The workflow advances only then.

## Consequences

- A step has an identity (`soul`), so "sceptical triager" and "adversarial
  reviewer" are configuration rather than prompt-wrangling.
- Cost control belongs to the step: budgets are per step and per sub-agent.
- The unit of authoring is larger and slower to write than a prompt node. The
  builder UI exists to make that tolerable.
- The unit of durability is the step (ADR 0005), which constrains what a step
  may do: it must be idempotent in effect, because it can re-run.
