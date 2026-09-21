# ADR 0012 — Steps run as a DAG; retry is a step-level policy

- **Status:** accepted
- **Date:** 2026-09-22

## Context

Steps ran strictly in sequence. For a bug fix that is mostly right — reproduce
before fix, fix before review. At organisation scale it is wrong: a survey, a
dependency audit and a lint pass have no reason to wait for each other, and a
five-step workflow takes the sum of its steps when it could take the longest.

Separately, the only retry available was the completion gate's `max_attempts`,
which retries the **model** inside one step. It cannot help a step that failed
because a network call or a tool died.

## Decision

**A step may declare `needs: [ids]`.** If any step does, the workflow is a DAG:
every step whose dependencies are satisfied runs concurrently, bounded by
`max_parallel` (default 4) and by the engine-wide run cap. If no step declares
`needs`, execution is sequential and byte-identical to before.

**A step may declare `retry: {max_attempts, backoff_sec}`**, which re-runs the
whole step — its tools and its workspace effects included.

Both execution modes share one `runOneStep`, so approval, the judge pass, gates
and retry cannot drift between them.

## What the DAG deliberately refuses

`skip_to` is rejected at load time in a DAG. A forward jump is a sequential
idea; once steps run concurrently, "skip ahead to X" has no defined meaning for
the steps already running. The error says so and points at `needs` plus a
`fail`/`needs_input` gate instead. Refusing is better than half-honouring it.

Cycles are detected at load time and the error names the path (`a → b → a`),
because "there is a cycle" is not actionable.

## Consequences

- **A step with a retry policy must be idempotent in effect.** This is the same
  constraint ADR 0005 imposes for resume, now with a second reason.
- A failing step stops the run; its dependents never start, and steps already
  running are allowed to finish rather than being killed mid-tool-call.
- Measured on a fan-out of three: peak concurrency 3, all steps `done`. The
  claim is tested, not asserted.
- Cost scales with concurrency. `max_parallel` is per workflow and the engine's
  run cap is global, so a burst cannot thrash the machine.
