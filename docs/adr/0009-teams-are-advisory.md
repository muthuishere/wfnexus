# ADR 0009 — A team is advisory; delegation is only structural when the parent lacks the capability

- **Status:** accepted
- **Date:** 2026-09-22

## Context

Declaring a `team` on a step gives its agent a `task` tool: a sub-agent runs on
a fresh transcript with its own scoped tools and returns one result, so a
research detour burns the child's context instead of the parent's.

`reproduce-bug` was given an `explorer` sub-agent and a prompt that said, in
terms, *"delegate code lookups to your explorer rather than reading widely
yourself"*.

Measured on a live run (Sonnet 4.5):

```
task calls : 0
bash calls : 19      read: 2   edit: 1
```

The step succeeded — it committed a failing test — and ignored the explorer
completely.

## Decision

Teams are kept where the sub-agent holds a capability the parent does not, and
treated as **advisory** otherwise. No workflow may be described as "using
sub-agents" merely because it declares one.

## Rationale

This is not disobedience and no prompt fixes it. A parent holding `bash` can
already `grep` and `cat`; delegation is strictly more expensive for it —
a round trip, a spawn, a summarised result — so the cheap path wins.

In unit tests a parent granted **no** tools delegates on the first turn, every
time. That is the configuration in which a team means something.

## Consequences

- Context isolation through delegation must be designed for: remove the
  capability from the parent, or accept that it will not happen.
- Because a parent with a shell can route around its own team anyway, forcing
  delegation by removing `read`/`grep`/`glob` alone does not work.
- Same shape as ADR 0006: a declaration that reads like a control is advisory
  until something enforces it.
