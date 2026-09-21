# ADR 0005 — The step is the durability boundary; we never call toolnexus resume

- **Status:** accepted
- **Date:** 2026-09-21

## Context

A run is long, multi-step and human-gated: it must survive a process restart.
toolnexus offers a durable-suspension path — a tool returns `Pending(Request)`,
the run halts, and `Runtime.Resume(answer)` continues it.

A spike against the live wire measured what resume actually does:

- it returns only `error` (no resumed result), and the suspended handle is not
  exposed by `Agent.Run`;
- it replays the turn **from the original prompt with an empty history**, so the
  leaf's own tools re-run — 1 invocation before, 3 after, the turn paid twice;
- the runtime is in-process and does not survive a restart at all.

Upstream's position (correct, and now documented there): SPEC pins
rewind-to-checkpoint by name, and the reattachment-by-task-key idempotency
guarantee covers `task` calls only — a leaf's own tools have no protection.

## Decision

**A step is the unit of durability, and we never call `Runtime.Resume`.**

A suspension is persisted as plain data (`step_runs.pending`), the run parks in
`needs_input`, and the HTTP request that started it returns immediately. When
the answer arrives — minutes later, another process — it is folded into the run
input and **the whole step re-runs from its prompt**.

Resume is a pure function of the database: replay the step list, skip
`done`/`skipped`, restart at the first that is not.

## Consequences

- **Steps must be idempotent in effect.** Derive branch names, do not increment
  them. This is the price of the decision and it is load-bearing.
- A re-run costs more than a resume would. Accepted: correctness across a
  restart beats saving a turn.
- We may never exercise upstream's durable-resume path, which is itself worth
  reporting — a feature nobody's real use case reaches is a finding.
