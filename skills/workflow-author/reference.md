# The shape, and one worked example

`wf_catalog` with `kind: "shape"` is authoritative — this file is orientation,
not a substitute for the call.

## A step

| field | meaning |
|---|---|
| `id` | referenced as `{{ .Steps.<id>.<field> }}`; hyphens are fine |
| `soul` | the agent's identity, second person, about character not task |
| `prompt` | a Go template over `.Input`, `.Steps`, `.WorkDir`, `.RunID` |
| `skills` / `tools` / `mcp` | allowlists — exactly what it may load, nothing more |
| `output_schema` | becomes the `submit_output` tool's input schema |
| `budget` | `max_turns`, `max_tokens`, `max_tool_calls`, `max_wall_sec` |
| `guardrails` | `deny` a tool, `args_contain` to narrow, `reason` shown to the model |
| `gates` | on the output: `needs_input`, `fail`, `skip_to` |
| `run:` | a command instead of an agent — output is `{ok, exitCode, stdout, stderr}` |
| `judge:` | a typed decision on the cheap tier |
| `env:` | variables; `${NAME}` reads one where the step runs |
| `runs-on:` | a worker label; omit it to run on the platform |

## One worked example

```yaml
name: dep-check
description: Is anything in this repository's manifests known-vulnerable?
on: { workflow_dispatch: {} }
jobs:
  scan:
    steps:
      # Cheap first: a command decides whether an agent is needed at all.
      - id: audit
        run: npm audit --json || true
        output_schema:
          type: object
          properties:
            ok:       { type: boolean }
            exitCode: { type: integer }
            stdout:   { type: string }
            stderr:   { type: string }

      - id: triage
        soul: >-
          You report only what the audit output shows, with package and version,
          and you never speculate about exploitability you cannot see.
        skills: [repo-navigator]
        tools: [read, grep, glob]
        budget: { max_turns: 15, max_tool_calls: 40 }
        guardrails:
          - deny: write
            reason: "this step reports; it does not change the repository"
        prompt: |
          Audit output:
          {{ .Steps.scan.audit.stdout }}

          Which of these actually reach production code in this repository?
        output_schema:
          type: object
          required: [reachable, summary]
          properties:
            reachable:
              type: array
              items:
                type: object
                required: [package, why]
                properties:
                  package: { type: string }
                  why:     { type: string }
            summary: { type: string }
        gates:
          - field: reachable
            equals: []
            action: fail
            message: "Nothing reachable — no action needed."
```

Note the two things that are easy to get wrong: a step inside a job is
referenced as `{{ .Steps.<job>.<step> }}`, and the `run:` step's output schema
is the fixed four fields, not something you invent.
