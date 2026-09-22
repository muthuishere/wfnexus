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

## The third escape, found by running it — 2026-09-22

The two rounds above both probed `bash`, because the guardrail governed `bash`
and a test named `TestContainmentOnlyGovernsBash` said so out loud. A live
`test-backfill` run with `isolate: true` then wrote an 18 KB `planner_test.go`
into **the platform's own checkout**, while its worktree received a 50-byte
stub. Nothing denied it, nothing reported it, and both the agent and the run
recorded success.

The call was not an attack and did not look like one:

```json
{"name": "write", "args": {"path": "apps/api/internal/planner/planner_test.go", "content": "…"}}
```

An honest relative path. toolnexus's `write` builtin resolves it with plain
`os.WriteFile`, which resolves against **the server process's working
directory** — and the server is started from the repository it is meant to be
protecting. The guardrail never looked at the call at all.

So the escape was not a hole in the enumeration; it was a whole tool family
outside the enumeration's scope. Two changes followed:

- every tool's path arguments are checked, not just `bash`'s, and a patch's
  internal `*** Add File:` paths with them;
- a relative path is **rewritten** to sit inside the workspace before the tool
  runs, so "relative" means relative to the workspace rather than to whatever
  directory the process happens to be in. That half is an inclusion rule, which
  is why it is the half that will keep working.

`TestContainmentOnlyGovernsBash` was inverted into
`TestContainmentGovernsEveryTool` rather than deleted: the test had encoded the
hole as intended behaviour, and that is worth keeping visible.

**The real conclusion is that this should not be solved here.** With ADR 0015's
child process running with its working directory set to the worktree, a relative
path lands inside by construction and `paths.go` stops being necessary. Every
fix in this ADR is the cost of not having that yet — and none of them needed a
sandbox to be the answer.

## Consequences

- **This is a guardrail, not a sandbox, and the distinction is not pedantic.**
  An agent with a shell has other exits: `env`, a symlink it creates, a Python
  one-liner, an editor's shell escape. The honest ceiling is "blocks the
  accident and the obvious".
- Real isolation needs a container or a chroot per run. Until that exists, no
  document may describe this platform as sandboxed.
- Generalising: **a parameter that expresses intent is not a control.** `workdir`
  and `team` (ADR 0009) both looked like controls and were advisory.
- And a second generalisation, earned the hard way: **the scope of a control is
  part of the control.** This guardrail was correct about every call it saw;
  it saw one tool out of ten.
