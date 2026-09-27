---
name: usage-prover
description: "Adversarially prove each dead-code candidate IS used before anything is deleted — literal search across code, docs, skills, templates and config, indirect use, build tags, entry points — then have a calibrated classifier (wfx judge) score each one. Sorts candidates into remove / keep / bug / needs-a-human. Trigger on: verify dead code, is this really unused, prove before removing."
---
# Prove it is used — and remove only what survives

Your job is to SAVE code. For each candidate, try hard to find what uses it.
Only a candidate that survives every attempt is safe to remove.

The finder already said "nothing calls this". The finder is often wrong in ways
that matter:

- **Indirect use** — a `String()` / `Error()` method called by `fmt`; an
  interface method called through the interface; reflection; `//go:linkname`.
- **Text that names it** — a template, YAML, a SKILL.md, a README, an ADR, a
  config key, a CLI flag, a route string. Removing the code breaks the text.
- **Other build contexts** — build tags (`//go:build windows`), other OSes,
  `cgo`, generated callers, a second module in the repo.
- **Entry points** — `main`, `init`, HTTP handlers registered by string, test
  helpers used by another package's tests, exported API that other repos use.
- **A LOST CALLER** — it looks dead because the call that should reach it was
  deleted by mistake. Deleting it then hides a bug. The tell: a live sibling
  that does almost the same thing, a feature the docs describe that nothing
  triggers, or `git log -S'<name>('` showing the call vanishing in a commit that
  was about something else.

## For each candidate, at least three independent checks

1. `git grep -n -w '<name>'` across the WHOLE repo — every file type, not just
   the language's. Note every non-definition hit.
2. The language's indirect paths from the list above (method sets, interface
   satisfaction, reflection, templates).
3. History: `git log -S'<name>(' --oneline` — when did the last caller go, and
   was that commit about this?
4. Where a graph exists: `ctx-optimize affected <symbol>`.

Record each check and its result. **"No reference found" from fewer than three
different checks is not proof.**

## Then the classifier — per candidate, calibrated

Write `evidence.jsonl`, one line per candidate: `{"id": ..., "state": "<the
candidate, its location, and what each check found, in plain sentences>"}`.
Then:

```sh
wfx judge -q ~/.claude/skills/usage-prover/assets/removal-questions.yaml \
  --items evidence.jsonl > verdicts.jsonl
```

(`assets/removal-questions.yaml` beside this file.) Add `--classifier <name>` to
use a specific JEV model this install has configured (`wfx registry
classifiers`), or `--model <id>` for any model id — the band cut points assume a
calibrated JEV model, so do not point this at a chat model and keep the table. Each line comes back with
`unused` (a probability and a band: no / uncertain / yes) and `kind` (remove /
keep / bug, with a confidence band). The bands are TypeSafe's self-consistency
cut: below 0.30 no, above 0.70 yes, between is a human's call.

## The decision table — apply it literally

| deterministic checks | `unused` band | `kind` | verdict |
|---|---|---|---|
| ≥3, all empty | yes | remove, sure | **remove** |
| any | any | bug (any confidence) | **bug** — report it, never delete |
| a real reference found | any | any | **keep**, citing the reference |
| anything else | uncertain, or `kind` uncertain | — | **needs-a-human** |

The classifier never overrules a reference you found: a grep hit is a fact, a
probability is a judgment. And it never promotes a candidate to **remove** on
its own — the deterministic checks come first.

## Output

`verdicts.md` and `verdicts.jsonl`: every candidate with its verdict, the checks
run and their results, the classifier's numbers, and for **keep** the reference
that saved it. Report counts per verdict. A list where everything is
**remove** means the checks were not really run.
