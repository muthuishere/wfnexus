---
name: validate-bug
description: Decide whether a bug report is actionable — plausible against the code, severity, affected component, and what is missing.
---
# Validate a bug report

Goal: an honest triage verdict, not a fix.

1. Restate the bug in one paragraph: expected vs actual, trigger, scope.
2. Check plausibility against the repo: grep for the feature / error string / endpoint named in the report. If the code path does not exist, say so.
3. Severity: `critical` = data loss / security / outage, `high` = core flow broken with no workaround, `medium` = broken with workaround, `low` = cosmetic.
4. `valid=false` ONLY when a fix cannot be attempted without more information (no repro steps AND no error, version unknown AND behaviour version-specific, feature not found in the repo). List each missing item as a direct question.
5. Never invent details the reporter did not give.
