# What is done, what is pending

Updated 2026-09-24. Kept current so "what's left" is read, not re-derived.

## Decisions recorded (21 ADRs)

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
| 0016 | an adapter is a registry entry, not code |
| 0017 | identity is the device grant (RFC 8628) |
| 0018 | the registry has a publish direction |
| 0019 | evals are the proof of portability, not a quality feature |
| 0020 | cost is a first-class output |
| 0021 | a pause names who may answer |

0016–0021 were all written on 2026-09-24, after the Mastra pass and one live
run. What they have in common is the pivot recorded in
[`docs/research/the-pivot-2026-09-24.md`](research/the-pivot-2026-09-24.md):
the defensible surface is **portability across execution backends** plus
**generation with a zero-token static proof**, and everything else we had been
claiming belongs to somebody else too.

## Deliberately not built

Sandboxes, egress proxies, snapshot resume, multi-worker scale, tenancy and a
secret store were all researched and are all **not now** — see
[`docs/not-now.md`](not-now.md), which records each one with the trigger that
would change our mind. The shape we are keeping is GitHub Actions: a workflow
file, jobs, steps, a working directory, a runner. Anything that adds a concept
outside that vocabulary has to be paid for by something actually going wrong.

The one hard gate in that document: **auth before this is reachable on any
address that is not localhost.** That gate is now held by construction rather
than by discipline — the default bind moved to `127.0.0.1:8090`
(`internal/config/config.go`), so the server ships loopback-only and an operator
who wants it reachable states `WFX_ADDR` and accepts what that means. ADR 0017
is what closes the gate properly; until it lands, anyone who can reach the port
is an admin, and the actor on an approval is a claim, not an identity.

## Run live, against a real model

Recorded because "it validates and loads" is not the same as "it works", and
every serious finding this project has had came from running something.

| workflow | result |
|---|---|
| `code-review` | **done**, 43 turns. Reviewed our own planner change and found a real cancellation leak. |
| `triage` | **done.** `classify` 14 turns, then the planner picked exactly one of three alternative producers of `triaged` — `investigate` ran, `answer` and `request-detail` correctly never did. |
| `test-backfill` | **both steps ran to completion**, then the `proven_failing` gate failed the run: tests were added without being watched to fail first. That is the gate working, not the platform breaking. |
| `code-review` (2026-09-24, `main~3..main`, sonnet-4.5) | **done.** `survey` 4 turns, `review` 27, both typed and validated. 31 LLM calls, 30 tool calls. Cost **$4.37** — 1,424,154 prompt against 6,541 completion, because there is no compaction and the 117 KB diff went every turn. |
| one step on local `qwen3:4b` via `ollama-http` | **done**, 2 turns, a true **$0.00**, `submit_output` still enforced. A 4B model on this laptop honoured the typed contract. |
| the registry's argv table, run for real | `ollama-cli` 2.9s, `opencode` 3.8s, `claude-cli` 4.7s, `codex-cli` 12.7s, `copilot-cli` 18.3s, `devin` (acp) 14.8s — all answered a live prompt through the shipped argv, and `ollama-http` returned a real 200. 23 providers load with zero skips; the other base URLs are doc-verified and their model ids are not. |

Two platform defects came out of these runs and are fixed:

- a step that spends its whole budget and never submits loses everything.
  Stating the budget in the soul was not enough — an agent cannot count its own
  turns — so the remaining count is now injected near the ceiling. `classify`
  went from failing at 15/15 to submitting at 14/15.
- the containment escape recorded in ADR 0006. Re-run after the fix, the written
  test file landed inside the worktree.

## Next, from the 2026-09-24 pivot

Ranked, in the order the pivot doc §5 sets:

1. **Distribution.** Tag, CI, and one install line. The client is the product
   and it is unobtainable. Nothing below matters if nobody can run it.
2. **Login → publish → registry.** `login` before `publish` despite "skill
   first", because publish needs an identity to publish *as*. Device flow
   (ADR 0017), then `wfx publish` for skills, MCP servers and workflows
   (ADR 0018).
3. **The run view, and the cost.** Spans, durations, tokens, one attempt = one
   span. The per-step half now exists (see the usage entry below); what is left
   is the run-level rollup and the screen.
4. **Evals** (ADR 0019). Promoted from a gap we skipped to the proof of the
   portability claim: "runs anywhere" is unverifiable until a workflow that
   passed on sonnet is shown to pass on `qwen3:4b`.
5. **Auth and authorization** (ADR 0017). Local users and roles first, default
   admin, pluggable authentication later; authorization is ours permanently.
6. **Compaction.** 1,424,154 prompt tokens against 6,541 completion on one
   `code-review` run — $4.37, most of it the same 117 KB diff re-sent every
   turn. This is a money problem now, not a capability gap.

Still good, still cheap, and none of it changes whether someone picks this over
Mastra: memoised `--recover` and `--only <step-id>`; `rhysd/actionlint` and
`nektos/act`'s `exprparser` instead of writing our own expression parser;
typed effects instead of a write token.

~~**Sell the dry run.**~~ **Done** — the README now shows its real output for
`code-review` and `bug-fix` and says what it checks. The claim was narrowed at
the same time: "nobody ships a static validation pass" is true of the **canvas
tier only** (6/6 test by really running). `mastra lint` exists, and for typed
edges `tsc` is a stronger check than `wfx dryrun` because it fails the build.
What neither can do is close a *generation* loop against a workflow that does
not exist yet.

**The risk to keep in view is GitHub, not Devin.** It owns the syntax, the
repository and the runner, and gh-aw is our shape one layer down. What would
make this project pointless is gh-aw gaining `needs:` between agentic jobs
*and* schema-validated hand-off between them. What it cannot easily take is
running the same workflow locally, off any forge, against a working tree, on
whichever model happens to be on that machine, with a check that costs nothing.

## Work pending (no decision needed)

- **A worker runs agent steps as well as commands.** The step's skills travel as files, with its
  tools, team, guardrails, budget and output schema; the worker runs the same engine code the
  platform does. The CLI does not travel — `provider: devin` is that machine's PATH and that
  machine's credential, which is the point. What cannot be placed: a step using the platform's own
  authoring tools, refused at pack time.
- **A worker has no sandbox.** It runs the command as the user it runs as. That is the bargain a
  self-hosted Actions runner and a Jenkins node both make, and it is why docs/not-now.md's sandbox
  entry now has a second trigger: a worker taking a job from a workflow we did not write.

- ~~**`cli` / `acp` providers still cannot execute a step.**~~ **Done.** toolnexus v0.19.0 exports
  the in-process transport and the pin is on it; a `cli` provider drives a real agent CLI, verified
  end to end on a worker. What was still missing was the dry run: it built the adapter without
  touching the program, so a `cli` provider whose binary is absent reported "would run" and then
  failed at the first turn with an exec error wrapped in an HTTP error — which reads like a network
  fault and is not. It is a PATH check now.
- **The workflow builder has not been exercised against a live API** end to end.
- ~~**No metrics endpoint. Nothing aggregates `MetricEvent`.**~~ **Half done.**
  `internal/engine/usage.go` aggregates every `MetricEvent` into a per-step
  accumulator — prompt/completion/total tokens, LLM and tool call counts,
  elapsed and LLM milliseconds, a per-model breakdown and `costUsd` — and
  writes it to `step_runs.usage` **while the step is still running**, coalesced
  on a 1-second deadline. `turns` is written live from the same accumulator, so
  the "what is this agent doing right now" defect (turns=0 through 29 calls) is
  gone. The write was measured, not assumed: 21µs per `PatchStep` on real
  SQLite, 25µs at concurrency 8. Free and unknown are distinct — a local model
  reports a true `costUsd: 0`, an unpriced provider reports `costUnknown: true`
  and no `costUsd` key.
  **Still missing:** no Prometheus endpoint and no OTEL export; no run-level
  rollup of the per-step figures; a step naming no `provider:` runs on the
  process-wide default, which is not a registry entry and therefore cannot
  carry a price.
- **Distribution: nobody can get `wfx`.** No git tags, no `.github/workflows`,
  no goreleaser, no npm package, no brew tap. `task release` builds 18 binaries
  onto one disk and publishes nothing. Under the pivot the client is the
  product surface, which makes this the headline problem.

## What we still do not use from toolnexus

All present in the pinned v0.19.0 and worth taking, most valuable first.

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
