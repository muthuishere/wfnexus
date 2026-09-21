---
name: reproduce-bug
description: Turn a validated bug into a deterministic reproduction, preferably a failing automated test committed in the repo.
---
# Reproduce a bug

Order of preference: failing unit/integration test → script → manual steps.

1. Find how the project runs tests (Makefile, Taskfile, package.json, go.mod, pyproject…). Run the existing suite once to know the baseline.
2. Locate the code path from the report (`repo-navigator` skill helps). Read it before writing anything.
3. Write the smallest test that fails BECAUSE of the bug and would pass once fixed. Name it after the bug. Put it next to the existing tests for that area.
4. Run it. Capture the failing output — that is your `evidence`. A test that fails for an unrelated reason (import error, missing fixture) is NOT a reproduction; fix the harness first.
5. Commit the test on the current branch with message `test: reproduce <bug>`.
6. `reproduced=false` only after genuinely trying at least two approaches; explain your best hypothesis.
