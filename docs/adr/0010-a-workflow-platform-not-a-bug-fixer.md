# ADR 0010 — This is a workflow platform; bug-fix is one workflow

- **Status:** accepted
- **Date:** 2026-09-22

## Context

The repository is called `bug-fixer-platform` and the first workflow is
`bug-fix`, so it is easy to read the product as "a bug fixer". That would be the
wrong shape to build toward: it makes the engine a detail of one pipeline, and
every new capability gets bent toward bug fixing.

What is actually general is the machinery: a step is an agent with a scoped
harness, steps hand each other schema-validated objects, gates branch on those
objects, humans approve where it matters, and everything is durable.

Nothing in that is about bugs.

## Decision

**The product is a platform for authoring and running agent workflows over a
repository.** `bug-fix` is the reference workflow — the one that exercises every
feature — not the product.

Genericity is demonstrated, not asserted: the platform ships several unrelated
workflows, and a new one is authored without touching Go.

| workflow | shape it demonstrates |
|---|---|
| `bug-fix` | the full pipeline: judge, teams, guardrails, approval, publish |
| `code-review` | read-only by construction — no step is granted a writing tool |
| `test-backfill` | a gate that refuses to start on an already-red suite |

Workflows are authored through `PUT /api/workflows/{name}`, which validates and
only writes if the definition survives a round trip through the real loader, and
through the builder UI over that endpoint.

## Consequences

- Nothing bug-specific belongs in `internal/engine`. Bug vocabulary lives in
  workflow YAML and skills.
- A capability is finished when a workflow other than `bug-fix` can use it.
- The repository name is now misleading. Renaming is deferred rather than
  denied — it costs remotes, clones and CI for a cosmetic gain, and the ADR is
  cheaper than the churn today.
