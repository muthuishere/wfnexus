# ADR 0015 — The runner is PID 1 of a step's execution unit

- **Status:** accepted
- **Date:** 2026-09-22

## Context

Today the engine **is** the executor. `runOneStep` builds the agent in the
server's own process, and every tool call the model makes — `bash` above all —
forks a child of the server. That has three consequences we keep paying for.

Isolation has nowhere to live. ADR 0006 could only offer a guardrail, because a
guardrail is the only thing you can install inside a process you are already
sharing with the work. Every escape that ADR records exists because the step and
the platform are the same process tree, with the same filesystem view and the
same environment.

Cancellation is advisory. Cancelling a run cancels a Go context; a `bash` tool
call that has already forked `go test` keeps running, because nothing owns that
process group. The step reports cancelled while its children are still burning
CPU.

And the engine cannot be restarted without taking the work with it. A deploy
kills every in-flight step, because the steps are goroutines in the binary being
replaced.

## Decision

**Introduce a runner that is PID 1 of the step's execution unit.** The engine no
longer executes a step; it *launches* one.

- The runner is a **subcommand of the same binary** (`wfnexus run-step`). One
  binary, two modes — the same choice ADR 0013 made for the API and ADR 0019
  will make for scale. There is no second artefact to version, and the runner
  cannot drift from the engine that launches it.
- The step spec arrives **on stdin as JSON**, and secrets arrive **in the
  environment by name only** (ADR 0021). Nothing about a step is passed on the
  command line, where it would be visible in `ps` to every user on the box.
- The runner **prepares the worktree once**, runs the agent to completion, and
  writes the result — the submitted output, the transcript, the diff — to stdout
  as a framed stream that the engine records. The engine's own database writes
  stay in the engine.
- The runner **forwards SIGTERM to its whole process group** with a grace
  period, then SIGKILL. Because it is PID 1 of that unit, the process group is
  exactly the step's descendants and nothing else.
- The runner exits with a **kind**, not just a code (ADR 0016a): could-not-start,
  ran-and-failed, or model-error. The engine's retry policy (ADR 0012) reads the
  kind.

## Why this is first

Everything else on the roadmap is easy once this exists and impossible while it
does not. A sandbox (0016) needs a process to put *inside* it. Egress policy
(0017) needs a network namespace owned by something with a lifetime. Snapshot
resume (0018) needs a defined moment when the unit's filesystem is quiescent.
Horizontal scale (0019) needs the unit of work to be launchable by any process
that claimed it, not only by the one that parsed the YAML.

It is also the one item that is genuinely hard to retrofit: it changes what a
step *is* at runtime, and every later decision either assumes it or works
around it.

## Consequences

- **A step becomes a process boundary as well as a durability boundary.** ADR
  0005 said the step is where we checkpoint; now it is also where we isolate.
  The two boundaries coinciding is what makes both simple.
- The cost is one `fork`+`exec` per step — 5.3 ms measured — against the current
  zero. Compared with a step that makes tens of LLM calls, it does not register.
- In-process test seams must survive. The engine's tests script the LLM
  transport (`UseTransport`) and never touch the network; a runner that can only
  be driven by spawning a process would destroy that. So the runner keeps an
  **in-process mode** used by tests and by `wfx` single-step runs, and the
  spawning path is what production uses. The same `runOneStep` serves both, for
  the reason ADR 0012 gives: two execution modes that do not share one function
  drift.
- Streaming gets harder before it gets easier: tool events now cross a pipe. The
  framing is part of this decision precisely so it is designed once rather than
  discovered under load.
