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

## Next, from the 2026-09-23 competitor pass

Ranked, with the reason each is cheap:

1. **Do not write an expression parser.** `if:` and `${{ }}` are GitHub's, and so are their
   semantics. `rhysd/actionlint` (MIT) parses and type-checks; `nektos/act`'s `exprparser` (MIT)
   evaluates with a typed environment and models the implicit `success()`. Writing our own is how
   a workflow ends up behaving differently here than in a repository's own Actions file.
2. **Resume by memoised step output.** `step_runs.output` already exists; add an input hash and
   ship `wfx run --recover` and `--only <step-id>`. This closes the snapshot-vs-replay question
   in docs/not-now.md — the answer is neither.
3. **Sell the dry run.** Six of six canvas builders test by really running. A static pass that
   costs no tokens is the clearest unoccupied ground in the survey and the README barely mentions
   it.
4. **Typed effects instead of a write token.** gh-aw's split is worth taking in the small: an
   agent step emits a typed effect object, and a privileged `run` step applies it — one generic
   `effects:` block with a cap, not their forty-handler catalogue. It turns prompt injection into
   a permissions problem without needing a sandbox.

**The risk to keep in view is GitHub, not Devin.** It owns the syntax, the repository and the
runner, and gh-aw is our shape one layer down. What would make this project pointless is gh-aw
gaining `needs:` between agentic jobs *and* schema-validated hand-off between them. What it cannot
easily take is running the same workflow locally, off any forge, against a working tree, with a
check that costs nothing.

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
