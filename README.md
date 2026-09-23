# wfnexus

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

Full analysis with sources: [`docs/research/competitors-2026-09.md`](docs/research/competitors-2026-09.md)
(and the earlier [`competitive-landscape.md`](docs/research/competitive-landscape.md)).

| | this | Archon | Windmill | n8n | GitHub agents | Devin/Jules |
|---|---|---|---|---|---|---|
| User-definable steps | YAML | YAML | flows | nodes | agent profiles | no (fixed loop) |
| Skills + tools scoped per step | yes | no (whole CLI) | per agent step | per agent node | **yes** (per profile) | per run |
| **Schema-validated hand-off, enforced *inside* the agent loop** | yes, stored `jsonb` | undocumented | after the step | parser node | no (`outputs:` are strings) | no |
| **Static check with no model call** (`wfx dryrun`) | yes | no | no | no | no | no |
| Runs locally, off any forge, against a working tree | yes | yes | yes | yes | no | no |
| Self-host | Go binary + PG + S3 | Bun | Rust + PG | Node (+Redis) | no | no |

Two honest corrections from the 2026-09-23 pass, because a comparison table that flatters us is
worse than none:

- **Per-step scoped tools is no longer a differentiator.** GitHub's custom agents take `tools:` and
  `mcp-servers:` in an agent profile under `.github/agents/`
  ([docs](https://docs.github.com/en/copilot/reference/custom-agents-configuration)). That row used
  to say "per run" for them. It was true when it was written and is not true now.
- **What survives scrutiny is narrower and more defensible.** The typed hand-off is enforced *in
  the loop* — `submit_output` is a tool whose validation failures come back as tool results, so the
  model corrects itself mid-turn. LangGraph's `response_format` lands on the final state and
  CrewAI's guardrails run after the task; Actions' own `outputs:` are untyped strings. And nothing
  else in the survey has a **static validation pass that costs no tokens**: six of six canvas
  builders test by really running.

## Run it

```bash
task infra:up     # Postgres :5460, MinIO :9030 (console :9031)
task api          # :8090 — migrates on boot, creates the bucket
task ui           # :5173 — Vite dev server, proxies /api
```

Needs `OPENROUTER_API_KEY` (or `OPENAI_API_KEY` + `LLM_BASE_URL`) in the environment. Copy
`.env.example` to `.env` for the rest. Nothing reads a secret into config — toolnexus picks the key
up at call time.

Or the whole thing in containers, including one worker:

```bash
docker compose -f infra/docker-compose.yml up -d    # → http://localhost:8090
```

Kubernetes manifests are in [`infra/`](infra/README.md). There is no chart and no operator: a
Deployment, a Service and a Secret.

## Where a step runs

A job says `runs-on: windows`. That is a **label**, never a machine — the same thing it means in
GitHub Actions, and the same thing a Jenkins node label means.

A machine joins by running one command, which the **Workers** page (or `wfx workers`) hands you:

```bash
wfx-runner join --url https://wfx.example.com --token wfx_… --labels windows,devin
wfx-runner run
```

From then on that machine takes the steps whose label it holds, and runs them with the toolchain
installed **there** — the Devin CLI, a JDK, a signing certificate, a licence dongle. The platform
never connects to it: the worker polls out. So the platform can be a pod behind an ingress and the
machine can be a laptop behind NAT, and neither has to be reachable from the other.

The result has exactly the shape a local `run:` step produces, so a gate reading `steps.build.ok`
cannot tell where it ran. Labels this process serves itself (`WFX_RUNNER_LABELS`, default
`local,self-hosted`) run in process, so a single-machine install needs no worker at all.

`wfx dryrun` says, for nothing, that a `runs-on:` nobody holds would wait:

```
STEP             KIND    RUNS ON                      BUDGET
build.whoami     run     buildbox (no machine)        30 turns
  warning build.whoami.runs-on   no worker online holds the label "buildbox" — this step would wait.
```

Today a worker runs `run:` steps; agent steps still execute on the platform, where the model
credentials and the tool loop are. [`infra/README.md`](infra/README.md) has the rest, including
running the worker as a service and what it does and does not isolate.

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
  cmd/wfx-runner/    the worker: joins a pool by label, takes steps, reports
apps/ui/             React + TS + Vite: workflow list, run form, live run view
infra/               Dockerfile, compose, k8s manifests — the whole deploy story
workflows/*.yaml     the workflows
skills/*/SKILL.md    the agent skills each step may load
mcp.json             MCP servers steps may be granted
```

Placement: `workers` (a machine and its labels) → `worker_jobs` (one step's work, queued for
whoever holds the label). `FOR UPDATE SKIP LOCKED` is what makes two machines on the same label
safe without a lease table.

State: `workflow_runs` → `step_runs` (status, attempts, turns, validated `output jsonb`, usage) →
`run_events` (the full activity log, replayed into the UI). Artifacts (agent transcript,
`git diff` of the workspace per step) go to S3 and are linked from the run.

## API

```
GET  /api/workflows                    POST /api/workflows/reload
POST /api/workflows/{name}/runs        GET  /api/runs            GET /api/runs/{id}
GET  /api/runs/{id}/events             SSE, ?after=<id> replays the backlog
POST /api/runs/{id}/approve|reject|input|retry|cancel
GET  /api/workers                      the pool + the join command
POST /api/workers/join|claim|heartbeat|jobs/{id}/result   the whole worker protocol
GET  /api/runs/{id}/artifacts/{id}     302 → presigned S3 (or ?inline=1)
```

## Status

Working end to end against a real repo with a real bug. Not yet done: a queue/limit for concurrent
runs, auth, and the "safe outputs" split that keeps a GitHub write token out of the LLM steps (see
the features-to-steal list in the research doc).
