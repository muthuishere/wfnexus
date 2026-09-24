# Field reference

`wf_catalog` with `kind: "shape"` is authoritative. This is orientation.

## A step, field by field

| field | meaning |
|---|---|
| `id` | referenced as `{{ .Steps.<id>.<field> }}`. Inside a job: `{{ .Steps.<job>.<step> }}` |
| `name`, `description` | for people; `description` also becomes the agent's `does` |
| `soul` | identity, second person, about CHARACTER not task |
| `prompt` | Go template over `.Input`, `.Steps`, `.Decide`, `.WorkDir`, `.RunID` |
| `skills`, `tools`, `mcp` | allowlists — exactly these, nothing more |
| `output_schema` | becomes `submit_output`'s input schema. The contract |
| `max_turns`, `budget` | `max_turns`, `max_tokens`, `max_tool_calls`, `max_wall_sec`, `max_children`, `max_concurrent`, `max_depth` |
| `max_attempts` | how many times the completion gate re-asks for a valid submission |
| `retry` | `max_attempts`, `backoff_sec` — re-runs the WHOLE step, so it must be idempotent |
| `guardrails` | `deny`, `args_contain`, `reason` |
| `gates` | on the output: `field`, `equals`, `action`, `skip_to`, `message` |
| `when` | `path`, `equals`/`exists` — skip the step unless the facts hold |
| `needs` | explicit dependencies (or use jobs) |
| `requires_approval` | stop for a human before this step |
| `ask_human` | give the agent an `ask_human` tool |
| `team` | sub-agents it may delegate to |
| `decide` | a cheap classifier pass BEFORE the agent |
| `judge` | a whole step that is only a classifier — no agent at all |
| `run` | a command instead of an agent |
| `shell` | `bash`/`sh`/`pwsh`/`powershell`/`cmd`; empty = the machine's best |
| `env` | `NAME: value` or `NAME: ${VAR}` (read where the step runs) |
| `runs-on` | a worker label; omit to run on the platform |
| `provider`, `model` | a registry name; omit for the default |
| `timeout_sec` | wall clock for a `run:` step |
| `consumes`, `produces` | named facts, for a goal-planned workflow |

Workflow level, not step level: `mount:` (folders the run needs) and `env:`
(which cascades into every step). The files beside a directory-form workflow
need no field at all. All three below.

## The three node kinds, cheapest first

**`run:` — no model, no cost.** Output is always exactly
`{ok, exitCode, stdout, stderr}`; do not invent a schema for it. A non-zero
exit is the command's ANSWER, not a failure — the step succeeds and a gate
decides what red means.

```yaml
- id: suite
  run: go test ./...
  timeout_sec: 600
  output_schema:
    type: object
    properties:
      ok:       { type: boolean }
      exitCode: { type: integer }
      stdout:   { type: string }
      stderr:   { type: string }
```

**`judge:` — a typed decision on the small model.** A whole step, no agent, no
tools. Use it for a verdict over text you already have.

```yaml
- id: route
  judge:
    state: |
      {{ .Steps.suite.stdout }}
    questions:
      flaky:
        type: noul                 # a 0..1 truth
        instructions: "Did this fail for a reason unrelated to the change?"
        true:  "a timeout, a port clash, a missing fixture"
        false: "an assertion about the behaviour under test"
      severity:
        type: score                # a level on an ordered rubric, 2-10 entries
        instructions: "How much does this failure matter?"
        levels: ["cosmetic", "annoying", "blocks a user", "data loss"]
      desk:
        type: choice               # one of your options
        instructions: "Who should see this?"
        options:                   # describe each by its CONSEQUENCE
          author:   "the person who wrote the change is asked to look"
          infra:    "the build machine is investigated, not the change"
          nobody:   "it is noise and the run ends here"
```

**`prompt:` — a full agent.** Only where judgement over a repository is
genuinely needed. It costs turns; everything above costs almost nothing.

## `decide:` — the cheap pass before an expensive agent

Same question types, but attached to an agent step and run FIRST, so a fraction
of a cent can decide whether the agent runs at all. Answers are readable in the
prompt as `{{ .Decide.<question> }}`.

```yaml
- id: investigate
  decide:
    state: "{{ .Input.report }}"
    questions:
      actionable:
        type: noul
        instructions: "Is there enough here to act on?"
    gates:
      - question: actionable
        below: 0.5                 # noul → below / at_least
        action: needs_input
        message: "The report is too thin. Ask for steps to reproduce."
  prompt: |
    Confidence this is actionable: {{ .Decide.actionable }}
    ...
```

Gate forms: `below:` / `at_least:` for `noul` and `score`, `is: <option id>`
for `choice`. Actions: `needs_input`, `fail`, `skip_to`.

## Gates on the output

```yaml
gates:
  - field: reproduced
    equals: false
    action: needs_input            # pause and ask a human
    message: "Could not reproduce: {{ .Output.hypothesis }}"
```

`fail` stops the run. `skip_to: <step-id>` branches. `needs_input` parks the
run durably — the answer can arrive hours later.

## Guardrails

First deny wins, and the model is SHOWN the reason as the tool result, so the
reason is the message:

```yaml
guardrails:
  - deny: bash
    args_contain: ["git push", "curl", "npm publish"]
    reason: "publishing is gated on human approval in this workflow"
  - deny: write
    reason: "this step reports; it does not change the repository"
```

## Teams

A sub-agent keeps research out of the parent's context:

```yaml
team:
  - id: explorer
    does: "Read-only research: finds where a symptom's code lives, reports the call path with file:line, never edits."
    soul: "You look things up and report exactly what you found. You never edit and never speculate beyond the source."
    tools: [read, grep, glob]
    budget: { max_turns: 20 }
```

## Shapes

- **`steps:`** — a flat list, sequential unless `needs:` says otherwise.
- **`jobs:`** — GitHub-Actions shaped: jobs run in parallel, `needs:` orders
  them, steps within a job are sequential. `runs-on:`, `env:` and `if:` cascade
  workflow → job → step.
- **`goal:` + `consumes`/`produces`** — the order is DERIVED and re-derived
  after every step. Do not also write `needs:`; they conflict.

## Folders a run needs — `mount:`

Workflow level, one line per folder, docker's spelling: `HOST[:AT][:ro]`.

```yaml
name: score-imports
mount:
  - /Users/me/datasets/2026:data:ro     # absolute, read-only
  - reports:out:rw                      # relative → the platform's data dir
steps:
  - id: count
    run: wc -l data/imports.csv && echo done > out/marker.txt
```

- **Read-only is the default.** `:ro` means the folder is **copied** into the
  workspace — there is no portable unprivileged read-only bind mount, so a copy
  is the only version of "read-only" that is true. The run can write to it and
  the real folder is untouched. Big folders are capped; point the mount at the
  subfolder you actually read.
- **`:rw` is real two-way access** to the user's folder, by symlink, and it is
  the only thing that widens the run's containment. Type it on purpose.
- **A relative HOST resolves under the platform's data dir** (`<work dir>/mounts/`),
  never the workspace and never the server's cwd. This is the portable spelling:
  it means the same thing on the server and on a worker.
- **A mount is never templated.** `{{ .Input.dir }}:data` is refused at load —
  otherwise whoever starts a run chooses which folder the workflow can read.
  Nor can it be a home directory, `.ssh`/`.aws`/`.gnupg`/`.config`, or the
  system tree.
- **On a worker** the *line* travels, not the folder. An absolute mount must
  exist on that machine or the step fails by name. `WFX_MOUNT_<AT>` is exported
  either way, so a script need not hardcode the path.

## Files a workflow ships — put them next to it

Write the workflow as a **directory** and everything beside it is staged into
the run's workspace. There is no `files:` key: the directory is the declaration.

```
workflows/
  report/
    workflow.yaml
    run.js
    lib/format.js
```

```yaml
# workflows/report/workflow.yaml
name: report
steps:
  - id: build
    run: node run.js
    timeout_sec: 120
```

`run.js` lands at the workspace root, so the step names it exactly as it is
written on disk. Flat `workflows/report.yaml` still works and carries no files.
A sidecar may not climb out of the workspace, and it may not land inside a
`mount:` — a workflow's own file never overwrites somebody's attached folder.

## Custom environment — `env:`

Workflow level and step level, both `NAME: value` or `NAME: ${VAR}`.

```yaml
name: score-imports
env:
  REPORT_TIER: nightly          # a literal: committed, so not a secret
  GH_TOKEN: ${GITHUB_PAT}       # a reference: read where the step runs
steps:
  - id: show
    env:
      REPORT_TIER: manual       # overrides the workflow's, only this name
    run: echo "$REPORT_TIER in $WFX_WORKSPACE"
```

Later wins, and only for the names it mentions:

| | where it comes from |
|---|---|
| 1 system | the stored env store, every run on this machine |
| 2 project | the stored env store, one repository |
| 3 run | `WFX_RUN_ID`, `WFX_STEP_ID`, `WFX_PROJECT`, `WFX_WORKSPACE`, `WFX_MOUNT_<AT>` |
| 4 workflow | `env:` at the top of the file |
| 5 step | `env:` on the step |

The same map reaches a `run:` step, an agent step's `bash` tool and its CLI
provider, here and on a worker. A credential written out in full is refused on
save — name it (`${GITHUB_PAT}`) and the value is read on the machine that runs
the step, never committed and never put on the wire.

## Triggers

```yaml
on:
  workflow_dispatch:                    # a person, the UI, or `wfx run`
  schedule:
    - cron: "0 3 * * *"                 # five fields, UTC
  repository_dispatch:
    types: [push, issue_opened]         # another system POSTs
```
