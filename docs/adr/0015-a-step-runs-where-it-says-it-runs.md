# ADR 0015 — A step runs where it says it runs

- **Status:** accepted
- **Date:** 2026-09-22

## Context

A GitHub Actions step has a working directory, and every relative path in it
means "relative to that". Nobody has to think about it, and no rule has to be
written down, because the runner's own working directory *is* the workspace.

Ours is not. The engine executes a step in the server's process, so a relative
path resolves against wherever the server was started — which is the platform's
own repository. That is not a theory:

- an agent `cd`'d into our checkout and ran `git stash` / `git reset` there;
- five absolute-path escapes were open in public code;
- a live `test-backfill` run wrote an 18 KB `planner_test.go` into our checkout
  through an ordinary relative `write`, while its worktree got a 50-byte stub.
  Both the agent and the run reported success.

Each was patched with another rule (ADR 0006), and each patch made the same
mistake in a new place: enumerating escapes instead of enumerating what is
included.

## Decision

**A step executes in a child process whose working directory is the step's
workspace.** That is the whole decision.

- The child is a **subcommand of the same binary** (`wfnexus run-step`). One
  artefact, two modes — the same shape as ADR 0013's one API and two front ends.
- The step spec arrives on **stdin**, not on the command line, where `ps` would
  show it.
- The child's **cwd is the worktree**, so a relative path is inside by
  construction. This is the fix for the whole escape class, and it is the reason
  to do it.
- Cancelling the step **signals the child's process group**, so a `go test` it
  started actually stops. Today it does not.
- The in-process path stays for tests and single-step `wfx` runs, driven by the
  same `runOneStep`, for the reason ADR 0012 gives: two execution modes that do
  not share one function drift.

## What this deliberately is not

**Not a container, not a namespace, not a sandbox.** Those are a different and
much larger decision, and this platform does not need one to stop being wrong
about where a file lands. A subprocess with the right working directory fixes
what actually broke, three times, for the cost of one `fork`+`exec`.

`internal/engine/paths.go` — which rewrites relative paths and checks every
tool's path arguments — exists only because this does not exist yet. When the
runner lands, most of it can go.

## Consequences

- **A step becomes a process boundary as well as a durability boundary**
  (ADR 0005). The two coinciding is what keeps both simple.
- Tool events and the submitted output now cross a pipe, so the framing is
  designed once here rather than discovered later.
- A step still runs with the server's user and the server's environment. That is
  a real limit and it is stated rather than hidden: this stops a step from
  landing in the wrong directory; it does not stop a determined one from reading
  the machine.
