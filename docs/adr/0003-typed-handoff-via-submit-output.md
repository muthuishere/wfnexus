# ADR 0003 — Steps hand each other validated objects, never prose

- **Status:** accepted
- **Date:** 2026-09-21

## Context

Every comparable product passes free text (or a branch name) between "plan" and
"implement". The downstream step then re-parses intent out of prose written by
a model that has already moved on.

We need `reproduce-bug` to be unable to hand work to `draft-pr` until it has
actually produced a reproduction — and to say so in a form a program can read.

Two mechanisms were considered:

1. **Parse the final assistant text as JSON.** Standard, and the failure mode is
   familiar: the model wraps it in prose, or emits a trailing comma, and the
   step dies *after* doing all the work.
2. **Make the contract a tool.**

## Decision

Each step's `output_schema` becomes the input schema of a native
`submit_output` tool. The model must call it. Validation happens **inside the
agent loop**: a rejected submission returns the validation errors *as the tool
result*, so the model corrects itself on the next turn. A toolnexus `Completion`
gate then refuses `done` until a submission was accepted, bounded by
`max_attempts`.

The accepted object is stored as `jsonb` and is what the next step's prompt
interpolates (`{{ .Steps.reproduce-bug.test_command }}`).

## Consequences

- A malformed result costs one turn, not one step.
- The hand-off is machine-readable by construction, so gates can branch on it.
- Prompts reference step outputs by path; a skipped step must render empty
  rather than explode (see `stepval` in `internal/workflow`).
- On success the final turn is a tool call, so `Outcome.Text` is empty — the
  accepted object is captured from our own tool, never read back off the result.
