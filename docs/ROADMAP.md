# What is done, what is pending

Updated 2026-09-22. Kept current so "what's left" is read, not re-derived.

## Decisions recorded (13 ADRs)

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

## Decisions still to take (pending ADRs)

Ranked. The first one is the only item that is hard to retrofit.

**0015 — the execution boundary (runner as PID 1).** Today the engine *is* the
executor, so isolation has nowhere to live. Define a runner that is PID 1 of the
step's execution unit, takes the step spec on env/stdin, prepares the worktree
once, runs the agent, and forwards SIGTERM to the process group with a grace
period. Ship it as a subcommand of the same binary, so we stay a single binary.
*Everything below becomes easy once this exists, and stays impossible while it
does not.*

**0016 — a real sandbox, and retiring ADR 0006.** With 0014 in place: stop
exec'ing `bash` on the host and exec a container instead — `--network=none
--read-only --cap-drop=ALL --pid=private`, the run's worktree as the only
writable mount. The `cd` escape that ADR 0006 records dies by construction,
because the platform's own repo is not in the mount namespace. Cost: a container
runtime on the worker host (rootless podman is cheapest); a VM for Mac dev.

**0017 — egress is deny-by-default.** A per-step allowlist through a CONNECT
proxy (the model endpoint, the git host, nothing else). No allowlist ⇒ no
network. A policy that fails to apply **fails the step**. This is the only real
defence against exfiltration; no guardrail can provide it.

**0018 — resume by snapshot instead of replay.** ADR 0005 accepts that an
answered `needs_input` re-runs the whole step from its prompt, which is what
forces every step to be idempotent. Snapshotting the worktree diff (we already
store it) plus the transcript would remove our sharpest correctness constraint.

**0019 — horizontal scale without a broker.** Postgres as the queue
(`SELECT … FOR UPDATE SKIP LOCKED` + a lease/heartbeat), N engine processes
claiming runs. No Redis, no broker, still one binary in two modes.

**0020 — authentication and tenancy.** There is none. Every endpoint is open and
every run is global. Nothing else on this list should ship publicly before it.

**0021 — the secret model.** `apiKeyEnv` keeps values out of workflow files,
which is right, but a provider is still machine-local. Moving providers into
Postgres with a secret-*name* indirection keeps the property and makes rotation
an API call rather than a redeploy.

## Work pending (no decision needed)

- **`code-review` and `test-backfill` have never been run.** They validate and
  load; that is not the same as working. Given that this session's two most
  important findings (ADR 0006, ADR 0009) were both invisible until something
  ran, treat them as unproven.
- **`cli` / `acp` providers have never executed a step.** They register and
  validate. Resolving one needs the devin adapter, parked behind the
  `toolnexus_inprocess` tag until upstream ships `InProcessTransport` (#95).
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
