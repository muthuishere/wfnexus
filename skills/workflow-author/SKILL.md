---
name: workflow-author
description: "Write or edit a wfnexus workflow: interview the person, choose the cheapest node that answers each question, draft, dry run your own draft, fix what it found, and only then hand it over. Trigger on: write a workflow, author a workflow, edit this workflow, add a step, change the output schema, make a workflow that…"
---
# Author a workflow

You do not hand over anything you have not checked. Three honest steps beat
seven you are guessing about. You never invent a name — you look it up — and
when the dry run disagrees with you, the dry run is right.

Two files sit beside this one: `reference.md` is the field-by-field shape with
worked examples, `interview.md` is what to ask before you write anything.

## Before you write: find out what you are building

Do not start from the first sentence someone gives you. Almost every bad
workflow is a good answer to a question nobody checked. Ask what you genuinely
cannot infer — usually four or five things, not twenty — and say what you are
assuming for the rest. `interview.md` has the questions and what each one
changes.

The two that matter most, because they change the shape rather than a field:

1. **What must be true for this to have worked?** That is the last step's
   `output_schema`. If the answer is "it ran", there is no workflow here — it
   is a shell script, and you should say so.
2. **What is irreversible?** Everything that touches the outside world goes
   behind `requires_approval` in ONE step at the end, and the steps before it
   get a guardrail denying it.

## Choose the cheapest node that answers the question

This is the judgement the whole platform exists for, and most authors get it
wrong by reaching for an agent every time.

| the question is… | use | cost |
|---|---|---|
| "what did this command return?" | `run:` | nothing |
| "which of these is it / how bad is it?" over text you already have | `judge:` | a fraction of a cent |
| "is this worth an agent at all?" before an agent step | `decide:` on that step | a fraction of a cent |
| "work this out by reading the repository" | a full agent step | turns, and real money |

A workflow that is three `run:` steps and one agent is usually better than four
agents. If you cannot say what an agent step would do that a command could not,
it should not be an agent step.

## Then: the loop

1. **`wf_catalog` with `kind: "shape"`, first.** That is the real structure of a
   workflow file. Do not guess field names from other workflow formats — this
   one has no `title`, no `type` and no `input` on a step.
2. **`wf_catalog` again for the names** — skills, tools, providers,
   classifiers. A name that is not there does not degrade; it makes the whole
   file fail to load.
3. **Draft it.**
4. **`wf_dryrun` your draft.** It reports what would actually happen here: the
   order, the rendered prompts, the turn ceiling, the variables each step
   reads, and anything unresolvable.
5. **Fix and dry run again** until it is clean.
6. **Dry run the exact definition you are about to submit** — not an earlier
   one. This is the most common way to get it wrong: fixing a draft, then
   submitting something that was never checked.
7. **Submit only then.** A draft you have not verified is not finished.

## What a good workflow looks like

- **A step is a whole agent, not a prompt.** A `soul` saying who it is, the
  tools it needs and nothing more, a budget, and an `output_schema` that is a
  real contract rather than `{summary: string}`.
- **A schema is a contract, so make it one.** `required:` the fields the next
  step reads. Use `enum` where the set is closed. `additionalProperties: false`
  unless you have a reason. A boolean called `ok` with nothing else is not a
  contract.
- **Steps hand each other typed objects.** A later step reads
  `{{ .Steps.<earlier-id>.<field> }}`, and that field must exist in the earlier
  step's `output_schema` — if it does not, the reference renders EMPTY and
  nobody finds out. The dry run catches this; that is what it is for.
- **Deny what the step must not do.** A step that only reads gets a guardrail
  denying `write` and `edit`, with a reason the model will be shown.
- **Every agent step gets a turn budget.** An agent told nothing spends
  everything, then fails having done the work but never submitted.
- **Gates are where a human belongs.** `needs_input` to ask, `fail` to stop,
  `skip_to` to branch, `requires_approval` before anything irreversible.

## Editing an existing workflow

Read it first — `wf_catalog` with `kind: "workflow"` and its name. Then change
only what was asked. A step id is referenced by every `{{ .Steps.<id> }}` after
it, so renaming one silently empties those prompts unless you update them too.
Dry run after an edit exactly as after a draft; an edit is not safer.

## Be honest about what it does not do

Your explanation says what the workflow does NOT cover and what you assumed.
An author who overstates a draft costs more than one who says plainly that a
step is a placeholder.
