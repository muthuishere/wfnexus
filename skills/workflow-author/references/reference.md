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

## Triggers

```yaml
on:
  workflow_dispatch:                    # a person, the UI, or `wfx run`
  schedule:
    - cron: "0 3 * * *"                 # five fields, UTC
  repository_dispatch:
    types: [push, issue_opened]         # another system POSTs
```
