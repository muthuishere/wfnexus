# ADR 0006 — Containment is a guardrail, and it is not a sandbox

- **Status:** accepted
- **Date:** 2026-09-22

## Context

Every run gets its own git worktree, and the engine pins every `bash` call to
that workspace by injecting `workdir`. This was believed to be containment.

It is not. On a live run the `draft-pr` agent executed:

```
cd /Users/…/wfnexus && git log --all --grep=reorder
```

and then `git checkout -b`, `git stash` and `git reset` — **in the platform's
own repository**. `workdir` was set correctly throughout; it sets only the
*initial* directory, and a `cd` in the command string leaves it. Nothing
errored. `bash` reported success. Every unit test passed, because none of them
modelled a shell that walks away.

It knew the path because the skills it loads live under the platform repo, so
their absolute paths were already in its context. The agent did not have to
find an exit; we handed it the map.

## Decision

A containment guardrail is applied to **every step and every sub-agent**, ahead
of the rules the YAML declares and regardless of what that YAML says (first deny
wins, so YAML can never widen it). A shell command whose `cd`, `pushd`,
`git -C`, `--git-dir` or `--work-tree` target resolves outside the workspace is
denied. `cd -`, `cd ~` and unexpanded `$VARS` are refused, because none of them
can be *shown* to stay inside.

Commands are tokenised rather than pattern-matched: Go's regexp has no
backreferences, and tokenising handles quoting honestly.

## Consequences

- **This is a guardrail, not a sandbox, and the distinction is not pedantic.**
  An agent with a shell has other exits: `env`, a symlink it creates, a Python
  one-liner, an editor's shell escape. The honest ceiling is "blocks the
  accident and the obvious".
- Real isolation needs a container or a chroot per run. Until that exists, no
  document may describe this platform as sandboxed.
- Generalising: **a parameter that expresses intent is not a control.** `workdir`
  and `team` (ADR 0009) both looked like controls and were advisory.
