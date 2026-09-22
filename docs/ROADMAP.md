# What is done, what is pending

Updated 2026-09-22. Kept current so "what's left" is read, not re-derived.

## Decisions recorded (22 ADRs)

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
| 0015 | the runner is PID 1 of a step's execution unit |
| 0016 | a real sandbox, one per run; ADR 0006 retired as the security model |
| 0016a | the three failure kinds are SET, never derived |
| 0017 | egress is deny-by-default |
| 0018 | resume by snapshot, not by replay |
| 0019 | horizontal scale without a broker (Postgres `SKIP LOCKED` + leases) |
| 0020 | authentication and tenancy |
| 0021 | the secret model |

0015–0021 are **recorded, not implemented**. Each states the decision and its
consequences so that nothing built before them assumes the opposite; the
implementation order is the order they are numbered, because 0015 is what makes
the rest possible.

## Why 0015 stopped being optional

A live `test-backfill` run wrote an 18 KB file into the platform's own checkout
through a `write` call with an ordinary relative path, because the builtins
resolve a relative path against the server process's working directory. That is
the third escape ADR 0006 has recorded and the second class of mechanism it
missed entirely. It is patched (`internal/engine/paths.go`) and the patch is the
same shape as the last two. With the runner as PID 1 with cwd = the worktree,
the whole file stops being necessary.

## Implementation order for 0015–0021

Ranked. The first is the only item that is hard to retrofit, and everything
below it becomes easy once it exists and stays impossible while it does not.

| # | what shipping it means | blocked on |
|---|---|---|
| 0015 | `wfnexus run-step` as a subcommand; step spec on stdin; SIGTERM to the process group; an in-process mode kept for tests | — |
| 0016 | one sandbox per run holding the run's worktree; `--read-only --cap-drop=ALL --network=none`; Seatbelt on the Macs, failing closed | 0015 |
| 0016a | three set failure kinds on every executor; retry reads the kind | 0015 |
| 0017 | CONNECT proxy owned by the runner; per-step host allowlist; no allowlist ⇒ no network | 0015, 0016 |
| 0018 | snapshot = worktree diff + transcript + pending request; resume through `ConversationStore` | 0015 |
| 0019 | `wfnexus worker`; claim with `FOR UPDATE SKIP LOCKED`; lease + heartbeat | 0015 |
| 0020 | every request authenticated; `tenant_id` as a required store argument | — |
| 0021 | providers in Postgres; secrets write-only; injected by name and by need | 0015, 0020 |

0020 has no technical blocker and is the gate on ever exposing this publicly.

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
