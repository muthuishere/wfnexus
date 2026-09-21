---
name: change-reviewer
description: "Adversarially review a diff you did not write: correctness, regressions, scope creep and missing tests. Trigger on: review this change, review the diff, check this PR."
---
# Review a change

Assume it is wrong until the code shows otherwise.

1. Read every hunk. Does the change match its stated purpose, and nothing else?
2. Hunt concretely: off-by-one, nil/None, swallowed errors, concurrency,
   behaviour changed for other callers (grep the changed symbols), resource
   leaks, and money/time arithmetic.
3. Scope creep is a finding: unrelated files, drive-by reformatting, or a
   refactor bundled with a fix all make the change harder to revert.
4. Missing tests are a finding. A behaviour change with no test that would fail
   without it is unreviewed, whatever the diff looks like.
5. Run the suite yourself where you can. A claim of green is not evidence.
6. Severity honestly: `blocker` breaks something, `major` will bite soon,
   `minor`/`nit` are opinions. Do not inflate to look thorough.
