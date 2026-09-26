---
name: workflow-scout
description: "Turn a product's recurring manual work into wfnexus workflow candidates that are WORKABLE, not toys — each checked against the 7-point bar (real trigger, system of record, typed output, verification, human gate, no silent failure, a success number), matched to a proven pattern from the top-6-per-category catalogue, and ranked by value. Trigger on: what workflows should we build, propose automations for this repo, workflow ideas for this product."
---
# Scout workflows worth running

Input: a product map with `recurring_work` (from product-cartographer).
Output: candidates a person can say yes or no to in one read.

## The bar — a candidate must clear ALL seven, or it is dropped (say which clause)

1. **Real trigger** — schedule, webhook, repository_dispatch, a failing CI run.
   "Chat with it" is not a trigger.
2. **System of record** — reads/writes the repo, the database, the issue
   tracker, an inbox, a payment provider — not a demo sheet.
3. **Typed output** — a PR, an issue, a report, a staged change, with a schema.
4. **Verification before done** — tests, a re-check against the source, a
   calibrated classifier over evidence (JEV via `judge:` or `wfx judge`).
5. **Human gate before anything irreversible or outward** — merge, send, pay,
   delete, deploy, file publicly. (Healthcare/insurance: never auto-deny.)
6. **No silent failure** — retries; uncertain → a person (JEV band 0.30–0.70).
7. **A success number** — time saved, cycle time, merge rate, incidents caught.

## Match to a proven pattern

The catalogue `top-6-by-category.md` — carried beside the workflow that uses
this skill (look in the working directory first), and kept at
`docs/scenarios/top-6-by-category.md` in the wfnexus repo — lists
102 researched workflows with triggers, steps, gates and cited numbers. For
each candidate name the closest entry (e.g. `ci-failure-doctor`,
`dep-upgrade-repair`, `code-remover`, `docs-drift-fixer`, `error-to-fix-pr`,
`release-notes-from-merges`) and what differs here. Borrow its shape; do not
invent a new one where a proven one fits.

## Per candidate

id (kebab-case), title, category (one of the platform's 17), the recurring
work it removes (with the evidence path), trigger, steps tagged
[run]/[prompt]/[judge]/[approval], the human gate and why there, verification,
success number (a baseline from the evidence if one exists), skills needed
(existing names first; new ones proposed with a one-line job), the closest
catalogue pattern, readiness (runs today / needs X), and the bar check — one
line per clause.

## Ranking

Value (how often × how painful, from evidence) × readiness today. Three
excellent candidates beat twelve plausible ones. Never propose what
`existing_automation` already does — propose FIXING it if it is broken.
