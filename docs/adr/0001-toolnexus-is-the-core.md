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

## Consequences

- We inherit behaviour we did not write and cannot fully see. That is the point:
  it is specified and tested across seven ports, and our tests are consumer
  tests over it rather than reimplementations of it.
- When a primitive is wrong, the fix is upstream, not a local workaround. Seven
  issues were filed and fixed this way (#87–#93); see ADR 0008.
- We are exposed to upstream's release cadence. Mitigated by pinning (ADR 0007).
