# bug-fixer-platform

**Typed pipelines for coding agents.** One Go binary, your Postgres, your repo.

A workflow is a YAML file. A step is a **prompt + a scoped set of agent skills/tools + a JSON-schema
output contract**. The engine runs each step as a [toolnexus](https://github.com/muthuishere/toolnexus)
agent, refuses to advance until the step's output validates against its schema, persists every step's
state in Postgres, streams the agent's tool calls to the UI over SSE, and stops for a human where the
workflow says so.

The first workflow is bug fixing: **validate → reproduce → draft PR → validate PR → publish PR.**

```
  validate-bug ──▶ reproduce-bug ──▶ draft-pr ──▶ validate-pr ──▶ [human approval] ──▶ finalize-pr
   skills:          skills:           skills:      skills:                             skills:
   validate-bug     reproduce-bug     fix-author   pr-reviewer                         pr-publisher
   repo-navigator   repo-navigator    repo-nav
   ↓ gate                ↓ gate                        ↓ gate
   valid=false →     reproduced=false →            approved=false →
   needs_input       needs_input                   fail (retry from draft-pr)
```

## Why this and not n8n / Devin / Copilot

Full analysis with sources: [`docs/research/competitive-landscape.md`](docs/research/competitive-landscape.md).
The short version — nobody combines all of these in one runtime:

| | this | Archon | Windmill | n8n | Copilot/Devin/Jules |
|---|---|---|---|---|---|
| User-definable steps | YAML | YAML | flows | nodes | no (fixed loop) |
| Skills + tools scoped **per step** | yes | no (whole CLI) | per agent step | per agent node | per run |
| **Schema-validated** hand-off between steps | yes, stored `jsonb` | undocumented | yes (generic) | parser node | no |
| Approval / needs-input gates | per step | yes | yes | wait node | PR review only |
| Self-host | Go binary + PG + S3 | Bun | Rust + PG | Node (+Redis) | no |

The wedge is the **typed hand-off**: `reproduce-bug` cannot pass work to `draft-pr` until it has
emitted `{reproduced, method, test_command, evidence, …}` and that object validated. Free-text
hand-off between "plan" and "implement" is what every other coding agent does.

## Run it

```bash
task infra:up     # Postgres :5460, MinIO :9030 (console :9031)
task api          # :8090 — migrates on boot, creates the bucket
task ui           # :5173 — Vite dev server, proxies /api
```

Needs `OPENROUTER_API_KEY` (or `OPENAI_API_KEY` + `LLM_BASE_URL`) in the environment. Copy
`.env.example` to `.env` for the rest. Nothing reads a secret into config — toolnexus picks the key
up at call time.

## How a step works

```yaml
- id: reproduce-bug
  skills: [reproduce-bug, repo-navigator]     # only these SKILL.md files are loaded
  tools:  [bash, read, write, edit, grep, glob, todowrite]   # only these builtins exist
  mcp:    []                                  # per-step allowlist from mcp.json
  max_turns: 40
  prompt: |
    Validated bug summary: {{ .Steps.validate-bug.summary }}
    …
  output_schema:                              # becomes the `submit_output` tool's input schema
    type: object
    required: [reproduced, method, steps, evidence, root_cause_hypothesis]
    properties:
      reproduced: { type: boolean }
      method:     { type: string, enum: [failing_test, script, manual, not_reproduced] }
      …
  gates:
    - field: reproduced
      equals: false
      action: needs_input     # pause the run and ask the human
      message: "Could not reproduce. {{ .Output.root_cause_hypothesis }}"
```

The contract is enforced *inside* the agent loop, not after it:

1. The step's `output_schema` becomes the input schema of a native `submit_output` tool.
2. When the model calls it, the value is validated. Failures come back **as the tool result**, so the
   model self-corrects in the same loop instead of the run dying.
3. A toolnexus `Completion` gate refuses to let the step finish until a submission was accepted —
   up to `max_attempts`, then the step stops loudly.

Prompts are Go templates over `.Input`, `.Steps.<step-id>.<field>`, `.WorkDir` and `.RunID`
(hyphenated step ids work — they are rewritten to `index` lookups).

Gates: `needs_input` (pause, ask), `fail` (stop), `skip_to` (jump).

## Isolation

Each run gets its own **git worktree** of the target repo (`isolate: true`, the default), so N runs
can work the same repository in parallel without fighting over the index or HEAD. `repo_url` clones
per run instead. Set `isolate: false` to let the agent work directly in your checkout.

## Layout

```
apps/api/            Go: engine, workflow loader, Postgres store, MinIO blobs, REST+SSE
  internal/engine/   the run loop, per-step agent, schema validation, event broker
  internal/workflow/ YAML loader + prompt templating
  migrations/        golang-migrate SQL (embedded, applied on boot)
apps/ui/             React + TS + Vite: workflow list, run form, live run view
workflows/*.yaml     the workflows
skills/*/SKILL.md    the agent skills each step may load
mcp.json             MCP servers steps may be granted
```

State: `workflow_runs` → `step_runs` (status, attempts, turns, validated `output jsonb`, usage) →
`run_events` (the full activity log, replayed into the UI). Artifacts (agent transcript,
`git diff` of the workspace per step) go to S3 and are linked from the run.

## API

```
GET  /api/workflows                    POST /api/workflows/reload
POST /api/workflows/{name}/runs        GET  /api/runs            GET /api/runs/{id}
GET  /api/runs/{id}/events             SSE, ?after=<id> replays the backlog
POST /api/runs/{id}/approve|reject|input|retry|cancel
GET  /api/runs/{id}/artifacts/{id}     302 → presigned S3 (or ?inline=1)
```

## Status

Working end to end against a real repo with a real bug. Not yet done: a queue/limit for concurrent
runs, auth, and the "safe outputs" split that keeps a GitHub write token out of the LLM steps (see
the features-to-steal list in the research doc).
