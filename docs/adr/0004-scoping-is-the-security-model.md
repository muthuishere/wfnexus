# ADR 0004 — Scoping is the security model

- **Status:** accepted
- **Date:** 2026-09-21

## Context

A step that only needs to read code should not be able to write it. The obvious
lever is the toolkit: give each step exactly the skills, built-ins and MCP
servers its YAML lists.

## Decision

Per-step allowlists for skills, built-in tools and MCP servers, enforced when
the toolkit is built. Sub-agents are scoped the same way and independently —
the explorer gets `read`/`grep`/`glob`, the author gets `write`/`edit`/`bash`.

Workflows are validated against the skill registry and the built-in catalogue at
**load** time, so a typo is a boot error rather than a mid-run surprise.

## The trap this ADR exists to record

`tn.SelectBuiltins` only *removes* a built-in whose entry is explicitly `false`.
A map containing just the allowed names leaves **every other tool switched on**.

The first implementation did exactly that. A step declaring `tools: [read, grep]`
was silently handed `write`, `edit`, `apply_patch` and `bash`. It looked correct,
passed review, and had no failing test.

`skills.BuiltinAllowlist` now writes `false` for every name outside the
allowlist, and a test asserts that a `bash`+`read` step is offered nothing else.

## Consequences

- Scoping is only as good as the enforcement point, and the enforcement point
  was wrong once. Prefer a test that asserts *absence* over one that asserts
  presence.
- Scoping bounds capability, not behaviour. A step with a shell can still leave
  its workspace (ADR 0006) and can decline to delegate (ADR 0009).
