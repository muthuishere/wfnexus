# Architecture

## The one-line version

**toolnexus is the core.** Every agent primitive — the loop, tools, skills, MCP,
sub-agent teams, guardrails, budgets, suspension, the classifier — is toolnexus.
This platform adds exactly three things toolnexus deliberately does not have:
**durable state**, **a workflow above the agent**, and **a UI**.

```
┌──────────────────────────────────────────────────────────────────────┐
│  apps/ui (React)      build workflows · run them · watch the loop    │
│                       read judge decisions · approve · answer        │
└───────────────────────────────┬──────────────────────────────────────┘
                     REST + SSE │
┌───────────────────────────────▼──────────────────────────────────────┐
│  apps/api (Go)                                                        │
│                                                                       │
│   workflow   ordered steps, typed hand-off, gates, approvals          │
│   engine     runs one step at a time, durably, resumable              │
│   store      Postgres: runs · steps · events · artifacts              │
│   blob       S3/MinIO: transcripts · diffs                            │
│   skills     the registry a step picks its skills from                │
│                                                                       │
│   ── everything below this line is toolnexus, not ours ──             │
│   agents.Agent · Spec{Soul,Tools,Team,Budget,Guardrails,Completion}   │
│   agents.Runtime (spawn/wake/wait/interrupt/close/resume)             │
│   tn.Toolkit (skills · builtins · MCP · native) · tn.Client (loop)    │
│   tn.Classifier (typed decisions on a small model)                    │
└──────────────────────────────────────────────────────────────────────┘
```

**The rule:** if toolnexus has a primitive, we configure it — we never reimplement
it. A pull request that hand-rolls retries, a tool loop, schema coercion, a
sub-agent, or an approval mechanism is wrong by construction.

## What a workflow is

A workflow is an ordered list of **steps** in YAML. A step is **not a prompt** —
it is a whole agent, with its own loop:

```
step = soul                      (identity / system prompt)
     + prompt                    (the task, templated from prior steps)
     + skills[]                  (allowlist from the skill registry)
     + tools[]                   (allowlist of toolnexus built-ins)
     + mcp[]                     (allowlist of mcp.json servers)
     + team[]                    (sub-agents it may delegate to via `task`)
     + guardrails[]              (policy: may it call this tool?)
     + budget{}                  (turns · tokens · tool calls · wall clock)
     + decide{}                  (classifier questions, before the agent runs)
     + output_schema             (the contract it must satisfy to finish)
     + gates[]                   (what the workflow does with that output)
```

The step runs until its agent **submits an output that validates**, or a limit
stops it loudly. Then the workflow advances.

## The five mechanisms that carry the design

### 1. The typed hand-off — why steps compose

Each step's `output_schema` becomes the input schema of a native `submit_output`
tool. The model must call it. Validation happens **inside the agent loop**:
failures return as the tool result, so the model self-corrects on the next turn
instead of the step dying. A toolnexus `Completion` gate then refuses `done`
until a submission was accepted — bounded by `max_attempts`.

The accepted object is stored as `jsonb` and is what the next step's prompt
interpolates (`{{ .Steps.reproduce-bug.test_command }}`). **Steps hand each other
typed objects, never prose.** This is the wedge — every comparable product passes
free text or a branch name between "plan" and "implement".

### 2. Scoping is the security model

A step sees exactly the skills, built-ins and MCP servers its YAML lists.

> **Non-obvious and load-bearing:** `tn.SelectBuiltins` only *removes* a built-in
> whose entry is explicitly `false`. A map containing just the allowed names
> leaves every other tool **on**. `skills.BuiltinAllowlist` therefore writes
> `false` for every name outside the allowlist. Getting this wrong silently hands
> a read-only step `write`, `edit` and `apply_patch`. There is a test that fails
> if it regresses.

Sub-agents are scoped the same way, which is the point of using them: the
explorer gets `read`/`grep`/`glob`, the author gets `write`/`edit`/`bash`.

### 3. The loop is per step, and it is toolnexus'

`agents.Agent` + the runtime give us, for free: the tool-calling loop, per-agent
souls, team delegation through the built-in `task` tool (fresh transcript per
child, one result back, usage rolled up), hierarchical budgets enforced live, and
a closed status vocabulary — `done · pending · incomplete · interrupted ·
closed · timeout · error`.

A limit stop is never silent: `incomplete` always carries `stoppedBy` in words
and `limit` as a value to branch on. The engine copies that verbatim into the
step's `error`; it never invents a status.

### 4. Human-in-the-loop is suspension, not a side channel

Two different pauses, deliberately kept distinct:

| pause | trigger | mechanism | run status |
|---|---|---|---|
| **approval** | `requires_approval: true` on a step | the engine halts *before* the step runs — the model is never called | `awaiting_approval` |
| **question** | the agent itself suspends mid-run (`question` built-in, or an MCP elicitation) | toolnexus `pending(Request)`; we supply **no** `WaitFor`, so the run returns a durable halt | `needs_input` |
| **gate** | a step's validated output trips a `needs_input` gate | the workflow, not the agent, asks | `needs_input` |

The durable halt is the important one: the `Request` is plain serializable data,
so the HTTP request that started the run returns immediately, the question is
persisted, and the answer can arrive hours later in another process. That is the
same contract whether the human is present or not — we chose the "later" posture
everywhere, because a web UI has no thread to block.

### 5. The judge tier — decide cheaply before spending

Not every decision deserves a frontier model. `tn.Classifier` asks pre-declared,
typed questions (`noul` 0..1 · `choice` over described options · `score` on an
ordered rubric) and returns calibrated numbers with nothing to parse.

A step's `decide:` block runs **before** its agent, so a workflow can route or
halt for a fraction of a cent instead of a full agent run — "is this report
actionable", "which component owns this", "how risky is this diff".

Four rules we inherit and must not forget:

1. **Describe every option by consequence.** Options named only by their id rank
   at chance. This is the whole feature.
2. **A decision is advisory and never authorises anything.** Thresholds and
   allowlists stay in Go `if` statements.
3. **Read `calibrated` before thresholding.** A threshold tuned on one backend
   does not transfer to another.
4. **Tests use the `static` backend.** Live answers move run to run; no test may
   assert a live number.

## Durability — what the platform actually adds

toolnexus' runtime is in-process. The platform's job is to make a multi-hour,
multi-step, human-gated run survive a restart.

```
workflow_runs   id · workflow · status · input(jsonb) · current_step · base_ref
step_runs       run_id · step_id · position · status · attempts · turns
                · prompt · output(jsonb, VALIDATED) · usage · error
run_events      append-only activity log — every tool call, result, LLM turn
artifacts       S3 keys: per-step transcript + workspace diff
```

Resume is a pure function of that table: the engine replays the step list,
skips `done`/`skipped`, and restarts at the first one that is not. A step is the
unit of durability — an interrupted step re-runs from its prompt, which is why
steps must be **idempotent in effect** (branch names derived, not incremented).

Each run gets its own **git worktree** of the target repo, so concurrent runs
never fight over the index or HEAD.

## UI

The admin UI is the product surface, not a debug view:

- **Workflows** — the registry of workflow YAML, each step showing its skills,
  tools, team, budget and output contract.
- **Skills** — the skill registry across roots (project, `~/.claude/skills`,
  `~/.agents/skills`), including what was *skipped* and why, so a bad frontmatter
  is visible instead of a skill silently missing.
- **Run** — the step timeline, the live agent activity over SSE (every tool call
  and result), the judge's decision for the step, the validated output next to
  its schema, and the artifacts.
- **The human's two buttons** — approve/reject an outward-facing step, and answer
  a `needs_input` question as a form generated from the question itself.

## Method: spike, then implement

Capabilities are proven against the **real OpenRouter wire** in `spikes/` before
the platform depends on them — teams, guardrails, suspension, the classifier, and
fail-fast/retry behaviour each get a throwaway program whose output is recorded
in `docs/spikes/`. Findings drive the implementation; the docs are not the wire.

## Non-goals

- Not a general workflow engine. Steps are coding agents; the schema is opinionated.
- No orchestration server, queue broker or Redis. One Go binary, Postgres, S3.
- No reimplementation of anything toolnexus ships.
