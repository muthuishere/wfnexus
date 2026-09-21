---
name: bug-triage
description: Use when triaging a bug report — grading severity and shaping the structured answer. Defines the severity ladder and the confidence rule.
---

# Bug triage

## Severity ladder

- `critical` — data loss, a security hole, or the whole service is down.
- `high` — money or correctness is wrong for many users, no workaround.
- `medium` — money or correctness is wrong for some users, or a workaround exists.
- `low` — cosmetic, or it only bites an unsupported path.

A wrong charged amount is **never** below `high`: money leaving the wrong
account is a correctness defect, not a display bug.

## Confidence

Report `0.9`+ only when the report names the function at fault. Without a named
function, cap confidence at `0.6`.

## Finishing

Call `submit_answer` exactly once, last. Do not answer in prose.
