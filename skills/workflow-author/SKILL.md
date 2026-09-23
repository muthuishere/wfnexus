---
name: workflow-author
description: "Write or edit a workflow for this platform: read the catalogues, draft, dry run your own draft, fix what it found, and only then hand it over. Trigger on: write a workflow, author a workflow, edit this workflow, add a step, change the schema, make a workflow that…"
---
# Author a workflow

You do not hand over anything you have not checked. Three honest steps beat
seven you are guessing about. You never invent a name — you look it up — and
when the dry run disagrees with you, the dry run is right.

## The loop

1. **`wf_catalog` with `kind: "shape"`, first.** That is the real structure of a
   workflow file — the exact field names a step has. Do not guess them from
   other workflow formats: this one has no `title`, no `type` and no `input` on
   a step.
2. **`wf_catalog` again for the names.** Every skill, tool, provider and
   classifier you use must come from it. A name that is not there does not
   degrade — it makes the whole file fail to load.
3. **Draft it.**
4. **`wf_dryrun` your draft.** It reports what would actually happen here: the
   order, the rendered prompts, the turn ceiling, and anything unresolvable.
5. **Fix and dry run again**, until it is clean.
6. **Dry run the exact definition you are about to submit** — not an earlier
   one. This is the most common way to get it wrong: fixing a draft, then
   submitting something that was never checked.
7. **Submit only then.** A draft you have not verified is not finished.

## What a good workflow looks like

- **A step is a whole agent**, not a prompt: a `soul` saying who it is, the
  tools it needs and nothing more, a budget, and an `output_schema` that is a
  real contract rather than `{summary: string}`.
- **Steps hand each other typed objects.** A later step reads
  `{{ .Steps.<earlier-id>.<field> }}`, and that field must exist in the earlier
  step's `output_schema`. If it does not, the reference silently renders empty
  and nobody finds out.
- **Cheap before expensive.** A `run:` step calls no model and costs nothing —
  use it for anything a shell command can decide. A `judge` step is a typed
  decision on a small model. Reach for a full agent step only where judgement
  over a repository is genuinely needed.
- **Deny what the step must not do.** A step that only reads gets a guardrail
  denying `write` and `edit`, with a reason the model will be shown.
- **Every agent step gets a turn budget.** An agent told nothing spends
  everything.
- **Gates are where a human belongs.** `needs_input` to ask, `fail` to stop,
  `skip_to` to branch. Anything irreversible goes behind `requires_approval`.

## Editing an existing workflow

Read it with `wf_catalog` (`kind: "workflow"`, `name: …`) before changing a
line. Then change only what was asked: a step id is referenced by every
`{{ .Steps.<id> }}` after it, so renaming one silently empties those prompts
unless you update them too. Dry run after the edit exactly as after a draft —
an edit is not safer than a draft.

## Be honest about what it does not do

The explanation you hand back says what the workflow does NOT cover, and what
you had to assume. An author who overstates a draft costs more than one who
says plainly that a step is a placeholder.

See `reference.md` for the field-by-field shape and a worked example.
