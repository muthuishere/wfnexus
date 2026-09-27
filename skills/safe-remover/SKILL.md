---
name: safe-remover
description: "Delete only the code an approved removal plan names, one commit per removal citing its evidence, cleaning up what the removal orphans (its tests, its docs), and proving the build and tests stay green after each one. Trigger on: remove the dead code, apply the removal plan, delete these unused functions."
---
# Remove what was approved — and nothing else

You get a plan: candidates whose verdict is **remove** and that a person
approved. You delete exactly those. Anything else you notice goes in your
report, not in the diff.

## Per removal — one at a time, one commit each

1. Delete the definition.
2. Delete what the removal orphans, and ONLY that:
   - a test that exercises nothing but the removed code (name it in the commit);
   - an import, constant or helper used only by the removed code;
   - a doc, comment or SKILL.md line that names it — fix the text, do not leave
     it pointing at something that no longer exists.
3. Build and test the affected module(s): `go build ./... && go vet ./... &&
   go test ./...` for Go, the package's `build` + `test` for TS. Red means the
   removal was wrong: **revert it** (`git checkout -- .`), mark it
   `reverted: <the error>`, and move to the next. Never "fix forward" by
   editing other code to make a removal compile — that is a refactor nobody
   approved.
4. Commit it on its own:

   ```
   remove <symbol>: unreachable, <one-line evidence>

   Found by: <tool and its line>
   Checks: <the three+ checks usage-prover ran, and that each came up empty>
   Classifier: unused=<noul> (<band>), kind=remove (<confidence>)
   Also removed: <orphaned test/doc/import, or "nothing">
   ```

One commit per removal is what makes a cleanup PR reviewable and revertible: a
reviewer who doubts one removal reverts one commit, not the PR.

## Rules

- Work on a branch; never on the default branch.
- Never touch generated files, lockfiles or vendored code.
- Never delete a test to make the suite pass. A test that fails after a removal
  is evidence the code was used — revert the removal.
- Record the test count before the first removal and after the last. The only
  allowed drop is tests you deleted as orphans, each named.

## Output

`removals.jsonl`: per candidate, `removed` / `reverted` with the commit SHA or
the error; lines deleted; the before/after build and test results.
