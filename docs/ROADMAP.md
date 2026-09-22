# What is done, what is pending

Updated 2026-09-22. Kept current so "what's left" is read, not re-derived.

## Decisions recorded (15 ADRs)

| # | decision |
|---|---|
| 0001 | toolnexus is the core; where the boundary is, and the test for new features |
| 0002 | a step is a whole agent, not a prompt |
| 0003 | steps hand each other validated objects, never prose |
| 0004 | scoping is the security model |
| 0005 | the step is the durability boundary; we never call toolnexus resume |
| 0006 | containment is a guardrail, and it is **not** a sandbox |
| 0007 | pin the dependency; a live working tree is not a dependency |
| 0008 | fix upstream, verify from outside |
| 0009 | a team is advisory; delegation is structural only when the parent lacks the capability |
| 0010 | a workflow platform, not a bug fixer |
| 0011 | one registry type for everything a step names |
| 0012 | steps run as a DAG; retry is a step-level policy |
| 0013 | one API, two front ends |
| 0014 | the plan is derived, not written down (goal + consumes/produces, replanned each step) |
| 0015 | a step runs where it says it runs (child process, cwd = the workspace) |

## Deliberately not built

Sandboxes, egress proxies, snapshot resume, multi-worker scale, tenancy and a
secret store were all researched and are all **not now** — see
[`docs/not-now.md`](not-now.md), which records each one with the trigger that
would change our mind. The shape we are keeping is GitHub Actions: a workflow
file, jobs, steps, a working directory, a runner. Anything that adds a concept
outside that vocabulary has to be paid for by something actually going wrong.

The one hard gate in that document: **auth before this is reachable on any
address that is not localhost.**

## Run live, against a real model

Recorded because "it validates and loads" is not the same as "it works", and
every serious finding this project has had came from running something.

| workflow | result |
|---|---|
| `code-review` | **done**, 43 turns. Reviewed our own planner change and found a real cancellation leak. |
| `triage` | **done.** `classify` 14 turns, then the planner picked exactly one of three alternative producers of `triaged` — `investigate` ran, `answer` and `request-detail` correctly never did. |
| `test-backfill` | **both steps ran to completion**, then the `proven_failing` gate failed the run: tests were added without being watched to fail first. That is the gate working, not the platform breaking. |

Two platform defects came out of these runs and are fixed:

- a step that spends its whole budget and never submits loses everything.
  Stating the budget in the soul was not enough — an agent cannot count its own
  turns — so the remaining count is now injected near the ceiling. `classify`
  went from failing at 15/15 to submitting at 14/15.
- the containment escape recorded in ADR 0006. Re-run after the fix, the written
  test file landed inside the worktree.

## Work pending (no decision needed)

- **`cli` / `acp` providers still cannot execute a step.** A step naming one is
  now *refused* rather than silently run on the default model, which is the
  honest state. Making them run needs one thing from upstream: a toolnexus
  release that exports `InProcessTransport` (issue #95 — present on the
  `issues-86-93-adrs` branch, absent from the pinned v0.18.1). The adapter
  itself is written and tested, behind the `toolnexus_inprocess` build tag.
  **This is the only outstanding dependency on toolnexus.**
- **The workflow builder has not been exercised against a live API** end to end.
- **No metrics endpoint.** toolnexus emits `MetricEvent` per LLM call and tool
  call and we forward it to the event log; nothing aggregates it.
- **Repository directory still named `bug-fixer-platform`.** Module, binaries
  and docs are `wfnexus`; the directory and git remote are not.

## What we still do not use from toolnexus

All present in the pinned v0.18.1 and worth taking, most valuable first.

| capability | why we want it |
|---|---|
| **Context compaction** (`agents.Compactor`, a `BeforeLLM` hook) | a long step dies at the context limit today; we cap turns instead, which stops work rather than continuing it. The single biggest gap. |
| **Multimodal content parts** (`tn.File`, `tn.Bytes`) | a bug report is often a screenshot. We can only take text. |
| **HTTP / OpenAPI tools** (`tn.HTTPTool`) | a step can name skills, built-ins and MCP servers but cannot call a plain REST endpoint. This belongs in the registry as a fourth tool kind. |
| **Conversation memory** (`ConversationStore`) | would let a resumed step continue a transcript rather than replay a prompt — the mechanism ADR 0017 needs. |
| **Persona / agent home** (`ComposeSoul`, `MemoryTool`) | a soul assembled from a directory, with durable per-agent memory — a reviewer that remembers this repo's recurring mistakes. |
| **A2A remote agents** (`Toolkit.AddAgent`) | a step could delegate to an agent on another machine — the distributed version of ADR 0009's team. |
| **Streaming** (`Client.Stream`) | token-level output in the UI; we stream tool events only. |

**Not needed:** in-process models (the devin adapter covers that case), serving
ourselves as an A2A agent or MCP server (no consumer yet).
