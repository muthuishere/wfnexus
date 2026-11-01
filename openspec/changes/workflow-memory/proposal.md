## Why

State (`{{ .Workflow.x }}`) is explicit: a workflow remembers only the keys an author decided to write.
Everything else a run learns — what it was asked, what each step concluded, why it failed, what a human
rejected and why — dies with the run. The next run of the same workflow starts blind and repeats the
same mistakes, and an author has to anticipate every lesson as a state key in advance.

The owner's ask: *wfnexus gathers memory for all previous runs of a workflow and loads it again by
default, so start and end always have workflow memory; if they want, they can take others' as well.*

## What Changes

- **END of every run** (done, failed, cancelled — a rejection ends a run `failed`) the engine writes a
  deterministic **run record** for that workflow: inputs, outcome, each step's status and truncated key
  output, errors, human resolutions (approved / rejected / answered, by whom, with the reason). Values
  the run redacts and every env-store value are scrubbed before anything is written. Writing memory
  never blocks or fails a run.
- **`remember` tool** — every agent step with memory on can record an explicit lesson during the run.
- **START of every run** each agent step gets a bounded section, *"What previous runs of this workflow
  learned"* (lessons, recent runs newest-first, a rolling digest of older runs), appended to its prompt,
  and the same text as `{{ .Memory }}` in templates (a prompt that references `.Memory` places it itself
  and is not appended to).
- **Consolidation** — raw run records are capped (`retain`, default 20); older ones fold into one
  rolling digest per workflow that is itself capped; lessons are capped at `2×retain`.
- **YAML** at workflow and step level: `memory: false`, or
  `memory: { include: [workflow:<name>, workflow:<project>/<name>, project, global], budget: N, retain: N }`.
  A step can narrow or disable, never re-enable a workflow that opted out.
- **Surfaces** — `GET/DELETE /api/workflows/{name}/memory`, `DELETE /api/memory/{id}`;
  `wfx memory ls|show|forget --workflow w`; a Memory section on the workflow page; dry run shows the
  memory that would be loaded.
- **Storage** — one new table `workflow_memory` (Postgres 000021, SQLite 000015).

## Capabilities

### New Capabilities

- `workflow-memory`: what a workflow records about each run, what it loads into the next, how it is
  bounded, scoped, scrubbed and inspected.

## Impact

- `apps/api/internal/engine` (memory.go, step.go, state.go, engine.go setRun, dryrun.go),
  `internal/workflow` (Memory type, validation, TemplateData.Memory), `internal/store` (memory.go,
  migrations), `internal/api`, `cmd/wfx/memory.go`, the UI workflow page, docs.
- Not breaking: a workflow with no `memory:` key gets memory on; `memory: false` restores today's
  behaviour exactly.
