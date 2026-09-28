---
name: wfnexus-deploy
description: Take a wfnexus workflow that works locally to the team server, git-natively: propose, approve, publish a version, schedule it, verify it is installed and scheduled there, then dry-run it. Trigger: deploy this workflow, ship it to the team server, publish and schedule it, run it every Friday.
---
# wfnexus deploy

A workflow that worked on your machine is not deployed until the TEAM server has it,
reviewed, versioned, scheduled, and able to run. This skill does exactly that and
ends with `DEPLOYED <name>@<version> to project <p>` or `BLOCKED: <stage>`.

It assumes the server is up and you are logged in to it (`wfnexus-setup`), and that
the workflow already ran once locally (`workflow-author`, then `wfx run <name> -f`).

## The one rule

**A proposal is approved by a person.** The script stops at the proposal and prints
the approve command. You pass `--approve` only when the person told you to approve
THIS change in this conversation, and you pass `--as` naming yourself
(`--as "agent:claude (for muthu)"`), never their name. Same rule as `approval-desk`.

## Run it

```sh
# 1. schedule: put it in the file, UTC, five fields (Friday 09:00 UTC here)
#      on:
#        workflow_dispatch:
#        schedule:
#          - cron: "0 9 * * 5"
#            input: { ... }        # a timer has nobody to fill a form

# 2. propose, and stop for review
bash skills/wfnexus-deploy/scripts/deploy.sh friday-status.yaml --project team --version v1.0.0
#    -> WAITING: approve with  wfx workflow approve <id> --as "<you>"   (exit 3)

# 3. once the person says yes: approve that same proposal, publish, verify, dry-run
bash skills/wfnexus-deploy/scripts/deploy.sh friday-status.yaml --project team --version v1.0.0 \
     --approve --as "agent:claude (for muthu)"
```

What each stage does (`references/deploy.md` has every blocker):

| stage | command underneath | proves |
|---|---|---|
| validate | `wfx validate <file>` | the server accepts the definition |
| project | `wfx projects` | the team project exists (`wfx project new team` makes one; it is a git repo with a first commit) |
| propose | `wfx apply <file> --project team` | a branch and a commit on the project's repo, a PR when it has a GitHub remote; the classifier's review is shown, and it is advice, not a gate |
| approve | `wfx workflow approve <id> --as <you>` | merged into the project; the server reloads it |
| publish | `wfx publish <file> --version v1.0.0 --project team` | an immutable, content-addressed version on the host (`v1.0.0` and `1.0.0` are the same version). `--to <git remote>` publishes to a git registry instead: tree `workflows/<name>/1.0.0/`, tag `<name>/v1.0.0` |
| verify | `wfx workflows show <name>` | the SERVER's copy, loaded from the project, with `schedule 0 9 * * 5  next Fri ... 09:00 UTC` |
| dry run | `wfx dryrun <name>` | on the server: waves, where each step runs, the model, the budget ceiling. No model is called |

## After it is deployed

- A real run now: `wfx run <name> -f` (streams the log). The schedule fires on its own.
- What is waiting on a human: `approval-desk`.
- Another server takes the same version with `wfx pull <digest>`.

## Stumbles to expect (so you can say them before they happen)

- The classifier's review of a proposal can say `reject` for a change that is fine: it
  is judging the diff alone. It is shown, never enforced. Read it; do not hide it.
- `wfx publish` prints `[toolnexus] duplicate skill name ...` lines when the machine
  has several copies of a skill under `~/.claude/skills`. Harmless; the script filters them.
- Cron is UTC. "Friday 9am" in Chennai is `30 3 * * 5`.
- A `run:` step that fails still ends `done` with `ok: false`; read the output.
