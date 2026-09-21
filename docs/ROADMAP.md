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

**0016 — a real sandbox, and retiring ADR 0006.** With 0015 in place: stop
exec'ing `bash` on the host. Every escape in ADR 0006 dies by construction,
because the platform's own repo is not in the mount namespace.

Three things measured by a peer that change the obvious design — take them as
given rather than re-deriving:

- **One sandbox per RUN, exec per tool call — not a container per step.**
  `docker run --rm` is 307 ms against 50.8 ms for a warm `docker exec` and
  5.3 ms for fork+exec, so a per-call container is ~60× a fork and 30 tool calls
  is 9–30 s of pure overhead. Every fast published number in the field
  (E2B ~150 ms, Daytona ~90 ms) is a warm pool or a snapshot resume, never a
  cold start. This composes with what we already have: **a run owns a git
  worktree, so it may as well own the sandbox holding that worktree** — the
  lifetime is defined by something real instead of a new concept.
- **The hardening flags are nearly free.** Namespace setup is 7.94 ms and
  `--network=none` is 0.04 ms, so `--read-only --cap-drop=ALL --network=none
  --pid=private` costs almost nothing. The expense is the container lifecycle,
  not the isolation.
- **macOS Seatbelt is a real boundary available today** — unprivileged, ~12.4 ms,
  and it enumerates inclusions rather than escapes, which is why it closes the
  exact escapes ADR 0006 records. It is worth having on the dev Macs long before
  the container path lands. **But it cannot nest inside an already-sandboxed
  host.** If wfnexus itself ever runs inside a sandbox, that path becomes
  silently unavailable — so it must FAIL CLOSED: if the profile cannot be
  applied, refuse to run the step rather than running it unsandboxed. A
  containment mechanism that degrades quietly to "no containment" is worse than
  none, because the ADR will still say it is protected.

**0016a — the three failure kinds are SET, never derived.** A sandbox must tell
us which of "could not run at all", "ran and failed" and "the model's command
was wrong" happened, because the exit code cannot: a container running
`exit 125` is byte-identical to "no such image", and a macOS Seatbelt denial
surfaces as EPERM with the wrapper exiting 0. containerd is the shape to copy —
a create-failure is an error, an exit is an event that is never an error.
Cloudflare's `ContainerUnavailableError{reason, retryAfterMs}` is close to what
ADR 0012's retry policy needs; E2B gets it backwards, raising on a non-zero exit
and collapsing the first two kinds. Without this, retry cannot tell a flaky
worker from a bad command and will retry what can never succeed.

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
