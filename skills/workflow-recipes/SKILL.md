---
name: workflow-recipes
description: "Build a working wfnexus workflow by ANSWERING QUESTIONS, not writing YAML. Asks what work the person wants off their plate, matches it to a proven recipe (production watch, alert triage, CI failure doctor, dead-code remover, docs drift, dependency upgrade, verified PR review, issue triage, ticket triage and reply drafts, weekly KPI narrative, invoice/expense exceptions, NDA triage), asks only that recipe's questions one at a time with a default for each, refuses answers that would make it a toy, then assembles, validates and dry-runs the workflow and installs it only on a yes. Trigger on: I want to know when X starts failing, take this off my plate, build me a workflow, automate this for me, workflow from a recipe, set up a watch/triage/review."
---

# Workflow recipes — a workflow from answers

The person has work they want off their plate. They do not want a YAML file and
they should never have to read one to get a good workflow. A **recipe** is a
proven shape (from `docs/scenarios/top-6-by-category.md`, the researched
catalogue) with the questions that fill it in. You ask; they answer; you
assemble.

This skill is the fast path of `workflow-author`. Same manners — ask about the
work, never about the schema; say back the shape before drafting; the user
drives; never claim a dry run was clean when it was not. The difference is that
the shape is already chosen, so the interview is short. When no recipe fits,
hand over to `workflow-author` and say so.

## The flow — stop where it says STOP

**1. Ask what they want off their plate.** One open question, plain words:

> What's the piece of work you'd like to stop doing by hand — or the thing you
> want to find out about before a customer does?

Let them answer in their own words. Do not show a menu first.

**2. Match a recipe.** Read `recipes/INDEX.md`, pick the closest, and say it in
two lines: the recipe, and *why* it matches their words. If two fit, name both
and pick one ("I'd start with X because…"). If none fits, say so and switch to
`workflow-author` — do not bend a recipe until it lies. STOP for their yes.

**3. Ask ONLY that recipe's questions, ONE at a time.** Open the recipe file.
Each question carries a default in brackets — offer it: *"…? (default: every
15 minutes — say 'ok' to keep it)."* Skip any question they already answered.
Never ask a question the catalogue, the repo or `wfx` can answer — look it up
(`wfx registry skills`, `wfx projects`, `wfx env --project <p>` for NAMES of
secrets, the repo's build/test commands).

**4. Hold every answer to the workable bar** (below). If an answer would make
it a toy, say which clause it breaks, in one sentence, and ask again with a
better default. Examples of toy answers you must refuse:
- "just run it when I click" for something that happens on its own → no real trigger.
- "read from this sample CSV / a demo sheet" → not a system of record.
- "auto-send / auto-merge / auto-pay / auto-reject" → no human gate on an outward act.
- "just tell me it ran" → no typed output, no success number.
- "if the check can't connect, ignore it" → silent failure.

**5. Say back, three lines, then STOP** (workflow-author's close):
> **Shape** — the steps in order, which are commands, which are judges, which is the one agent.
> **Stops** — where it pauses for a person, and why there.
> **Assumed** — every default they accepted without discussing it.

**6. Assemble.** Copy the recipe's skeleton into a directory named after the
workflow (`<name>/workflow.yaml`), replace EVERY `{{UPPER_CASE}}` placeholder,
and copy the sidecars the recipe lists from this skill's `sidecars/` plus the
recipe's `questions.yaml`. Then prove nothing was left behind:

```sh
grep -nE '\{\{[A-Z_]+\}\}' <name>/ -r && echo "placeholders left — fill them"
```

Two rules the skeletons follow and your edits must keep:
- A step whose `needs:` was SKIPPED still runs (skipped counts as finished), so
  every step after a conditional one carries its own `when:`. Above all a
  `requires_approval: true` step: without a `when:` it parks the run for a
  person even when there is nothing to approve.
- A `run:` step's non-zero exit is its answer, not a failure: `exitCode` 3 means
  "something to look at", and the next step's `when:` reads it.

`{{ .Input.x }}` / `{{ .Steps.x.y }}` (with a dot) are the platform's own
templating and stay. `{{UPPER_CASE}}` (no dot) are yours to fill.

**7. Validate and dry-run — before anyone says yes.**

```sh
wfx validate <name>/workflow.yaml                            # schema, names, skills, providers
python3 <this skill>/scripts/dryrun-draft.py <name> -i k=v   # full dry run, NOT installed
```

`wfx dryrun <name>` only works on an INSTALLED workflow; `dryrun-draft.py`
posts the draft (with its sidecar files) to the server's `/api/dryrun` endpoint
so the person sees the dry run before anything is installed. Fix every ✗ and
re-run. Show them the waves, the steps, the turn ceiling and any `!` warning,
verbatim. Never summarise a failed dry run as clean.

**8. Install only on an explicit yes, into the right project.**
Which project: the one whose repository the workflow reads (`wfx projects`).
Then write the directory to that project's `.wfx/workflows/<name>/` (for the
platform's own project, `workflows/<name>/`) and reload:

```sh
curl -s -X POST 127.0.0.1:8090/api/workflows/reload
wfx dryrun <name>                                            # now installed — same result expected
```

(`wfx apply <file.yaml>` also installs, but it sends only the YAML — a recipe
with sidecars must be copied as a directory.)

Then tell them the four things workflow-author's hand-over names: where it
lives, what one run costs (turn ceiling), which gate will stop and ask them, and
the success number you will both watch. Offer a first cheap run
(`dry_run=true` where the recipe has it).

## The workable bar — every recipe clears all seven, and so must the answers

1. **Real trigger** — a schedule, an inbound event (`repository_dispatch`), a
   failing CI run. "When I remember" is not one.
2. **System of record** — the real database, repo, tracker, inbox, ledger. A
   read-only credential is fine and preferred; a copy or a sample is not.
3. **Typed output** — an issue, a PR, a labelled ticket, a draft, a report with
   a schema. Never "a summary".
4. **Verification** — tests re-run, the finding re-checked against the source,
   and a calibrated classifier (`wfx judge` over `questions.yaml`) on every claim
   before it acts.
5. **Human gate before anything outward or irreversible** — send, merge,
   publish, pay, delete, reject a person. `requires_approval: true` on that step.
   Internal and reversible writes (a label, an internal issue in their own repo)
   may act on the classifier's **yes** band only.
6. **Escalation on uncertainty, no silent failure** — JEV bands: `no` < 0.30 <
   `uncertain` < 0.70 < `yes`. **uncertain → a person decides** (`ask_human`),
   a check that could not run is itself a finding, a missing verdict counts as
   uncertain (`sidecars/route.py` does this).
7. **A success number** — agreed now, with a baseline: merge rate, precision
   of filed issues, minutes saved, caught-before-customer count.

## Sidecars this skill ships (copy the ones a recipe names)

| file | does |
|---|---|
| `sidecars/watch.py` | runs `signals.yaml` checks → `signals.json`; exit 3 = something to look at; a broken check is a finding |
| `sidecars/route.py` | `wfx judge` verdicts → `yes/uncertain/no.jsonl`; exit 3 = a person must decide, 0 = something to act on, 4 = nothing |
| `sidecars/file_issues.py` | one GitHub issue per fingerprint, deduped, comments at most daily, `DRY_RUN` |

## Secrets

A recipe NAMES a credential (`$PAYMENTS_DB_URL`, `$GITHUB_TOKEN`); it never
contains one. Check the name exists with `wfx env --project <p>` (values are
never shown). If it is missing, tell the person the exact command —
`wfx env set NAME --project <p>` — and let THEM run it. Never ask them to paste
a secret into the conversation.

## Recipes

`recipes/INDEX.md` — one line each, with the words people use for them.
