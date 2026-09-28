# Deploy: blockers and what fixes them

`scripts/deploy.sh` prints `ok  <stage>`, `WAITING: ...` (exit 3, a proposal needs a person),
`BLOCKED <stage>: <why>` (exit 1), or `DEPLOYED <name>@<version> to project <p>` (exit 0).

| stage | blocker | fix |
|---|---|---|
| args | --project / --version missing | Both are required. The version is immutable once published |
| args | --approve without --as | Name yourself: `--as "agent:claude (for <person>)"` |
| validate | the server rejects the file | The message names the field. Fix the YAML; `wfx validate <file>` again |
| project | no project on the server | `wfx project new <p>` (a fresh git repo with a first commit), or `wfx project add <repo url> --as <p>` for the team's existing repo |
| propose | saved directly instead of proposed | The project's repo has no commit yet (created by an older server). Commit once in it, then re-run |
| approve | refused | The proposal is no longer pending (someone rejected or merged it); `wfx workflow proposals --project <p>` |
| publish | 404 on /api/bundles | The server is bound to loopback: it has no users, so nobody to publish as, and the route does not exist there. Publish to the team server (the compose stack is not loopback), or `--to <git remote>` |
| publish | already published | Versions are immutable. Bump it: v1.0.1 |
| verify | the file has a schedule but the server's copy has none | The approved definition is not the one in your file. `wfx workflows show <name>` shows the path it loaded from |
| dryrun | would not run | The report names the step: a provider that is not ready, a missing env var (`wfx env set NAME`), a label with no worker |

## Where a PR comes from

A proposal on a project whose repo has a GitHub remote is pushed and opened as a PR with `gh`
(on the server). Approving merges it with `gh pr merge`. Without a remote, the branch and the
merge are local to the server's clone. Both are git; only the second needs no GitHub token.

## Schedules

```yaml
on:
  workflow_dispatch:          # keep it runnable by hand
  schedule:
    - cron: "0 9 * * 5"      # minute hour day-of-month month day-of-week, UTC
      input: { topic: "the week" }
```

The scheduler ticks once a minute, starts at most one run per matching minute, and does not
backfill a minute the server was down for. `wfx workflows show <name>` prints the next fire time
from the server's own copy.
