# Writing the skills a step loads

A template ships with **no skills on any phase, deliberately**: the five phases of
a bug fix are the same everywhere, and what a good bug report looks like in this
shop is not. So the shape is given and the expertise is the part the person adds
— which makes writing that expertise the most valuable thing you do here, and the
part nobody can do for them.

You are not choosing from a catalogue at this point. You are writing the file.

## What a step skill is

A directory with a `SKILL.md`, loaded into that step's context and nothing else's:

```
skills/validate-bug/SKILL.md
```

```markdown
---
name: validate-bug
description: Decide whether a bug report is actionable — plausible against the
  code, severity, affected component, and what is missing.
---
# Validate a bug report

Goal: an honest triage verdict, not a fix.

1. Restate the bug in one paragraph: expected vs actual, trigger, scope.
2. Check plausibility against the repo: grep for the feature / error string /
   endpoint named in the report. If the code path does not exist, say so.
3. Severity: `critical` = data loss / security / outage, `high` = core flow
   broken with no workaround, `medium` = broken with workaround, `low` = cosmetic.
...
5. Never invent details the reporter did not give.
```

Read `skills/validate-bug/SKILL.md` and `skills/repo-navigator/SKILL.md` in full
before writing one. They are short on purpose.

## The rules that actually matter

**One skill, one job, and name it after the job.** `validate-bug`, not
`bug-helpers`. A step loads a named set; a grab-bag means every step that wants
one paragraph of it loads all of it, and the scoping in ADR 0004 stops meaning
anything.

**Write the judgement, not the procedure.** The model already knows how to run
`grep`. What it does not know is that a `critical` here means data loss rather
than "the customer is shouting", or that this codebase keeps its migrations in
two dialects and a PR touching one and not the other is wrong. Every line should
be something a competent stranger to *this shop* would get wrong.

**Say what NOT to do.** "Never invent details the reporter did not give" is
doing more work than the four steps above it. The failure you have actually seen
is worth more than the procedure you imagine.

**Enumerate the vocabulary.** If the output schema has an enum, the skill should
say what each value means here. A schema can enforce that `severity` is one of
four strings; only the skill can say which one a broken export is.

**Keep it short enough to be read every turn.** It is in the context window for
the whole step, and it is re-sent every turn — the most expensive prose in the
system. A page that earns its place beats five that do not.

**Do not put a credential in it.** Ever. It travels with the bundle when the
workflow is published.

## How to get it out of the person

They know this; they have not written it down. Ask about the last failure, not
the general case:

- "What did the last agent get wrong that a person on your team would not?"
- "Someone files a bug that turns out to be a config mistake. What should happen?"
- "What does `critical` mean here — what was the last critical one?"
- "What would make you reject this PR in review, that a stranger would not know?"

Turn each answer into a line. An answer you cannot turn into a checkable line is
a conversation, not a skill.

## Then wire it

The skill exists on disk before the workflow names it: an unknown skill name is
a **boot error**, not a warning, so the workflow will not load at all.

```yaml
- id: validate-bug
  skills: [validate-bug, repo-navigator]
  tools:  [bash, read, grep, glob]
  output_schema: { … }
```

Check it with `wf_catalog` the way you check every other name, then `wf_dryrun`.
And when the workflow is published, the skills resolve **at publish time** and
travel inside the bundle — which is why a bundle runs on a host that has never
seen them, and why a skill that does not resolve is refused at publish rather
than discovered mid-run.
