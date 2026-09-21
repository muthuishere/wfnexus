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

## Measured, 2026-09-22 — what it actually blocks

A peer reported that `env -C <dir> <cmd>` would defeat the tokeniser. Probing it
rather than reasoning about it produced a more useful answer in both
directions.

`env -C`, `make -C` and `tar -C` were **already denied**: the scanner matches a
bare `-C` token whatever command precedes it, which the prose above understated
by naming only `git -C`. But the probe found five escapes that were wide open,
and they are worse, because **none of them changes directory at all**:

| probe | before | after |
|---|---|---|
| `cat /etc/passwd` | ALLOWED | denied |
| `echo pwned > /tmp/pwned` | ALLOWED | denied |
| `rsync -a . /tmp/exfil/` | ALLOWED | denied |
| `find / -name secret -execdir cat {} \;` | ALLOWED | denied |
| `python3 -c "os.chdir('/etc'); …"` | ALLOWED | denied |

An absolute path needs no `cd`. The guardrail was scanning for directory
*changes* and therefore could not see the simplest exfiltration there is. It now
also denies any absolute path resolving outside the workspace, with a read-only
allowance for `/usr`, `/bin`, `/lib`, `/System`, Homebrew and the standard
streams — an interpreter has to be reachable or nothing runs.

**That allowance is the honest weakness, and it restates the whole problem one
level down: it enumerates what may leave.** The rule this keeps proving is the
one the peer's research states best:

> every mechanism that fails works by **enumerating escapes**;
> every mechanism that works **enumerates inclusions**.

This guardrail enumerates escapes. It is therefore a filter, not a boundary, and
each round of probing will keep finding more — `find -execdir` took one probe to
find and there will be another. Do not read the table above as "now it is safe";
read it as "these five specific holes are closed".

## Consequences

- **This is a guardrail, not a sandbox, and the distinction is not pedantic.**
  An agent with a shell has other exits: `env`, a symlink it creates, a Python
  one-liner, an editor's shell escape. The honest ceiling is "blocks the
  accident and the obvious".
- Real isolation needs a container or a chroot per run. Until that exists, no
  document may describe this platform as sandboxed.
- Generalising: **a parameter that expresses intent is not a control.** `workdir`
  and `team` (ADR 0009) both looked like controls and were advisory.
