# ADR 0001 — toolnexus is the core; we add durability, a workflow, and a UI

- **Status:** accepted
- **Date:** 2026-09-21

## Context

The platform runs multi-step AI agents against real repositories. Every piece of
that — a tool-calling loop, MCP, agent skills, sub-agents, budgets, suspension,
a classifier — is something `toolnexus` already ships, byte-identically across
seven languages and pinned by a shared SPEC.

The temptation in a host is to wrap each of those in "our own" abstraction, for
control. That is how a host ends up with two implementations of a retry policy
that disagree under load.

## Decision

**toolnexus is the core. We configure its primitives; we never reimplement one.**

The platform adds exactly three things toolnexus deliberately does not have:

1. **Durable state** — the runtime is in-process; a run must survive a restart.
2. **A workflow above the agent** — toolnexus stops at one agent and its team.
3. **A UI** — authoring, watching, approving.

A change that hand-rolls retries, a tool loop, schema coercion, a sub-agent, or
an approval mechanism is wrong by construction and should be rejected in review.

## Where the line is

Verified, not assumed: toolnexus has no `Workflow` type, no dependency graph and
no `needs` — its own ADRs 0023–0028 are all agent, loop and answer contracts. It
is going **deep** (seven ports, byte-identical, spec-pinned), not **broad**.

| belongs to toolnexus | belongs to this platform |
|---|---|
| one agent and its team | the ordering *between* agents |
| the tool-calling loop | the workflow above the loop |
| tools, skills, MCP, A2A | which of them a step may name |
| suspension (`Pending`/`Answer`) | what a paused run *is* while it waits |
| budgets within an agent subtree | budgets across a run, and run concurrency |
| the classifier | when a judgment gates a step |
| in-process state | Postgres, S3, resume after a restart |
| — | authoring, approval, the UI |

**The test for a new feature:** does it concern *one agent executing*, or *many
agents arranged*? The first is upstream — file an issue, do not work around it.
The second is ours, and must not leak into upstream's shape.

Two live consequences of the line:

- `needs`, `max_parallel` and step retry are ours, and stay ours even though
  toolnexus has budgets and a completion gate that superficially resemble them.
  Theirs bound one agent; ours arrange many.
- Suspension is theirs and we use it unchanged — but *what a parked run means*
  (a row in `needs_input`, answerable hours later from another process) is ours,
  which is why we never call their `Resume` (ADR 0005).

## Consequences

- We inherit behaviour we did not write and cannot fully see. That is the point:
  it is specified and tested across seven ports, and our tests are consumer
  tests over it rather than reimplementations of it.
- When a primitive is wrong, the fix is upstream, not a local workaround. Seven
  issues were filed and fixed this way (#87–#93); see ADR 0008.
- We are exposed to upstream's release cadence. Mitigated by pinning (ADR 0007).
