---
name: cleanup-pr-writer
description: "Write a detailed, verifiable pull request for a dead-code removal: every removal with its location, evidence and risk, what was kept and why, bugs found, build/test counts before and after, and how to revert each commit — with every claim checked against the evidence by a calibrated classifier before it is published. Trigger on: write the cleanup PR, PR description for removals, describe this refactor PR."
---
# The cleanup PR — every claim traceable

A removal PR is trusted or it is reverted. The reviewer's question for every
line is "how do you know this was dead?" — so the body answers it, per removal,
with evidence they can re-run.

## The body — in this order

```markdown
## What this removes
<N> unreachable functions / unused identifiers / files / dependencies,
<L> lines. No behaviour change: build, vet and all <T> tests pass before and after.

## Removals
| # | What | Where | Found by | Why it is dead | Risk | Commit |
|---|------|-------|----------|----------------|------|--------|
| 1 | `Engine.applyDecideGates` | engine.go:854 | deadcode | superseded by `applyDecideGatesLinear` (schedule.go:334), the one the live path calls (schedule.go:185) | low | abc1234 |

## Kept on purpose
Candidates a tool flagged that are NOT removed, and what saved each one.

## Bugs found, not fixed here
Code that looks dead because a caller was lost. Deleting it would hide the bug.

## Needs a human
Candidates the evidence could not settle, with the classifier's probability.

## Verification
| | before | after |
|---|---|---|
| build / vet | ✓ | ✓ |
| tests | <T> pass | <T'> pass (<T-T'> removed as orphans: <names>) |
| lines | | −<L> |

## Reverting
Every removal is its own commit: `git revert <sha>` undoes one without the rest.
```

Use real numbers from `removals.jsonl` and `verdicts.jsonl`. A cell you cannot
fill from them says "not measured" — never an estimate.

## Before publishing: check every claim against the evidence

A PR body is where a confident sentence outruns the evidence. Check it, per
claim, with the calibrated classifier (TypeSafe's citation-check pattern):

1. Write `claims.jsonl` — one line per removal row and per "kept" row:
   `{"id": "<row>", "state": "CLAIM: <the row's why-it-is-dead text>\nEVIDENCE: <that candidate's checks and verdict from verdicts.jsonl>"}`
2. `wfx judge -q ~/.claude/skills/cleanup-pr-writer/assets/claim-questions.yaml --items claims.jsonl`
3. Any claim whose `supported` band is not `yes`: rewrite it to say only what the
   evidence shows, or move the removal to "Needs a human". Re-check.

Publish only when every claim is `supported: yes`. Say in the PR that the claims
were checked this way — a reviewer is entitled to know.

Publishing itself is `pr-publisher`'s job: push the branch, open the PR with
this body, return the URL.
