# edit — a workflow exists and should be different

## 1. Read it first

`wf_catalog` with `kind: "workflow"` and its name. Do not edit from what they
told you it does.

## 2. Find what the change touches

The risk in an edit is never the change — it is what the change breaks.

- **Renaming a step id** breaks every `{{ .Steps.<old-id> }}` after it. Those
  prompts do not error; they render empty. Grep the whole file for the old id.
- **Removing a schema field** breaks every prompt and gate that read it.
- **Adding a step in the middle** changes what the steps after it can see, and
  in a goal-planned workflow it changes the derived order.
- **Tightening a guardrail** can make a step that worked fail; loosening one is
  a decision the person should make knowingly, not a side effect.

Say what the change touches before you make it, and stop if it is more than
they expected.

## 3. Change only what was asked

Do not reformat, do not "improve" a neighbouring step, do not add the budget
you think is missing. Mention it; do not do it. An edit that also changes three
other things cannot be reviewed.

## 4. Dry run and hand over

`assets/steps/step-dryrun.md`, then `assets/steps/step-handover.md`. An edit is not safer
than a draft.
