# What goes wrong

In rough order of how often it actually happens. This is also the review
checklist: read a draft against these before saying it is good.

## 1. Everything is an agent

The expensive mistake, and the most common. Four agent steps where three
commands and one agent would do. An agent costs turns and real money; a `run:`
step costs nothing and a `judge:` step costs a fraction of a cent.

Ask of every agent step: *what does this do that a command could not?* If you
cannot answer in a sentence, it is not an agent step.

**Symptom:** every step has `prompt:`. **Fix:** anything with a deterministic
answer becomes `run:`; any verdict over text you already have becomes `judge:`.

## 2. The output schema is not a contract

`{summary: string}` tells the next step nothing and cannot be gated on. It is
the single biggest difference between a workflow that holds together and one
that does not.

A contract has: the fields the next step actually reads, `required:` on the
ones it cannot work without, `enum` where the set is closed,
`additionalProperties: false` unless there is a reason.

**Symptom:** no gate could be written against the output. **Fix:** name the
decision the next step has to make, and give it the field that makes it.

## 3. A prompt references a field that does not exist

`{{ .Steps.triage.severity }}` where `triage` never declared `severity`. It
does not error — it renders EMPTY, the agent reads a sentence with a hole in
it, and nobody finds out.

**Symptom:** an agent behaving as if it was told nothing. **Fix:** every
`.Steps.<id>.<field>` must appear in that step's `output_schema`. The dry run
catches this; that is what it is for.

## 4. Nothing stops for a human before something irreversible

A workflow that pushes, publishes, deploys or emails without an approval gate
is one bad run away from being switched off forever.

**Symptom:** a step with `bash` and no guardrail, near the end. **Fix:** one
step with `requires_approval: true`, and a guardrail on every earlier step
denying the same thing with a reason the model will be shown.

## 5. No turn budget

An agent told nothing spends everything, then fails having done the work but
never submitted it. This has happened for real more than once.

**Symptom:** an agent step with no `budget` and no `max_turns`. **Fix:** give
it one. It is also a message to the agent — it is told how many it has.

## 6. A read-only step can write

A step whose job is to establish facts should not be able to change them.
Denying `write` and `edit` costs one line and removes a whole class of surprise.

**Symptom:** `tools: [read, grep, write]` on a step called `investigate`.

## 7. The workflow was never dry run — or an earlier version was

Fixing a draft and then submitting something that was never checked is the most
common way to hand over a broken file. Dry run the exact thing you submit.

## 8. A gate that cannot fire

`equals: false` on a field that is a string, or a gate on a field the schema
does not declare. It silently never matches, and the workflow has no safety it
appears to have.

## 9. `retry:` on a step that is not idempotent

`retry:` re-runs the WHOLE step, including its tool calls and its side effects.
On a step that opens a PR, that is two PRs.

**Fix:** retry only steps that can be run twice with the same result, or make
the step check before it acts.

## 10. A credential written into the file

The file is committed. A value under a name like `GITHUB_TOKEN` is a leak the
moment it is saved, and it looks like ordinary configuration. Name it:
`GITHUB_TOKEN: ${GITHUB_PAT}` — the value is read where the step runs. The
platform refuses the literal form on save, but do not rely on that to notice.

## 11. Goal-planned and `needs:` at the same time

`goal:` derives the order from `consumes`/`produces`. Writing `needs:` as well
means two orders, and they conflict. Pick one.

## 12. A workflow with nothing to decide

The honest failure. If every step is deterministic and nothing branches, this
is a shell script or a CI job, and saying so is more useful than building it.
