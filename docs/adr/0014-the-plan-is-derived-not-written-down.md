# ADR 0014 — The plan is derived, not written down

- **Status:** accepted
- **Date:** 2026-09-22

## Context

ADR 0012 let a step declare `needs: [ids]`, which made execution a DAG and gave
us parallelism. It also made the author the planner: you hand-wire every edge,
and the shape is fixed before anything runs. A workflow cannot respond to what
its agents actually find — only a gate can stop it, never re-route it.

Embabel's model is the one worth taking: an action's preconditions and effects
are declared (there, inferred from parameter and return types), a goal is
declared, and the planner formulates the route — **replanning after every
action**, so the system can adapt to what the last action discovered.

We already had the missing half. A step's JSON-schema output contract (ADR 0003)
is exactly a postcondition, and it is *validated* before it can satisfy
anything.

## Decision

A step may declare **`consumes`** and **`produces`** — facts, not step ids — and
a workflow may declare a **`goal`** fact. When it does, the order is derived:

1. every step whose consumed facts hold, and whose `when` guard holds, runs;
2. ready steps run concurrently, bounded by `max_parallel`;
3. when a step completes, its output is asserted as its produced facts;
4. **the plan is computed again** against the new world;
5. the run ends when the goal fact holds.

A `when` guard reads a dotted path into a real produced value
(`triage.valid == false`), so the route follows what an agent found, not what
the author guessed.

Three ordering modes now exist, in increasing autonomy: derived plan → declared
DAG (`needs`) → plain sequence. **A workflow may use `needs` or facts, never
both** — the two would disagree the moment a guard fails, and a silent
disagreement about order is the worst failure available here.

## What it deliberately is not

Not a search. No cost model, no backtracking, no novel-path discovery. Planning
is "everything applicable, now", re-evaluated whenever the world changes.
A search would buy optimality we cannot define and cost us the thing that
matters more: a failure we can explain.

So `Stuck` is the centrepiece, not an afterthought. When nothing can run it
names each blocked step, the fact it is missing, and **whether any step could
ever produce it** — because "blocked" and "impossible" need different fixes.
`Validate` catches the impossible cases before anything runs and costs nothing.

## Consequences

- Authors declare *what a step needs and establishes*, which is local knowledge,
  instead of the global order, which is not.
- Adding a step cannot break an unrelated edge, because there are no edges.
- Two steps producing the same fact are alternatives, chosen by their guards —
  branching falls out rather than being a feature.
- The facts are only as honest as the schemas, which is an argument for keeping
  `additionalProperties: false` on step outputs.
- Every plan re-derivation is emitted as a `plan` event (ready set, known facts,
  goal), so the UI can show *why* a step ran. A derived order that cannot be
  explained would be worse than a hand-written one.
