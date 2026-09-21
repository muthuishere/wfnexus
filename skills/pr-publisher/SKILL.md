---
name: pr-publisher
description: Push an approved branch and open the pull request with gh, returning the PR URL.
---
# Publish the PR

Runs only after a human approved. Outward-facing — be exact.

1. `git status` must be clean; `git log <base>..HEAD` shows only the fix commits.
2. `git push -u origin <branch>`.
3. `gh pr create --base <base> --head <branch> --title "<title>" --body-file <tmpfile>` — write the body to a temp file first (multi-line markdown).
4. Read back `gh pr view --json url,number` — the output is the artifact, never the exit code.
5. If `gh` is not authenticated, say so in the result (`pushed=true`, `pr_url=""` is NOT allowed — fail loudly instead).
