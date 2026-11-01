## Why

A step today can `retry`, be guarded by `when`, and be ordered by `needs` or `jobs` — but it cannot
repeat. Two shapes of real work need it and are currently written as hand-copied steps or pushed into
a shell `for` loop where the platform cannot see them:

- **Fan-out over a list** — review each changed file, probe each service, triage each alert. The list
  comes from the run input or an earlier step's typed array output.
- **Repeat until a check passes** — fix, re-run the suite, fix again; poll until a deploy is healthy.

Both must stay inside the platform's guarantees: every unit of work persisted and visible, a bound that
cannot be forgotten, human gates that still pause, and a run status that never says `done` for work that
did not settle.

## What Changes

- **`foreach:`** on a step — a path (`input.files`, `list.items`) or a literal YAML list. The step (or a
  `steps:` body) runs once per item with `{{ .Item }}` / `{{ .Index }}` in templates; optional
  `parallel: N` caps concurrent iterations.
- **`until:` / `while:`** — guards in the existing `when` shape, checked after each iteration, with a
  **mandatory** `max_iterations`. A loop that exhausts its bound without settling FAILS.
- **`steps:` body** — a loop step may hold an ordered group instead of its own prompt/run.
- **Per-iteration rows** — every iteration is an ordinary step row (`review[2]`,
  `fix-loop[1].check`) executed by the existing step runner, so persistence, retry, gates, approvals,
  `approvers:` and resume apply unchanged. The loop step's output aggregates to
  `{iterations: [...], count, last}`.
- **Validation** rejects unbounded loops, more than one loop mode, foreach paths that do not resolve or
  are not arrays or run later, guards that read nothing the loop produces, nested loops, and body ids
  that shadow outer steps.
- **Dry run** shows each loop's mode and planned iteration row ids, and checks body templates as the
  first iteration would render them.
- **UI** lists iteration rows under their loop step on the run page; the builder validator mirrors the
  loop checks.

## Capabilities

### New Capabilities

- `workflow-loops`: how a step repeats — foreach, until, while, bodies, bounds, per-iteration
  persistence, aggregation, gates inside loops, and the dry-run plan.

### Modified Capabilities

None.

## Impact

- `apps/api/internal/workflow`: `Step` gains `foreach`, `parallel`, `until`, `while`,
  `max_iterations`, `steps`; `TemplateData` gains `Item`, `Index`; new `loop.go`.
- `apps/api/internal/engine`: `runOneStep` dispatches loops to `loop.go`; replays skip iteration rows;
  `Retry` resets by range so a finished loop keeps its record; dry run gains `DryLoop`.
- `apps/api/internal/store`: `ResetStepsRange`. No migration — iteration rows use the existing
  `step_runs` table with positions above every definition step.
- `apps/ui`: `Step` type, run-page iteration rows, builder validation.
- Docs: `site/src/content/docs/concepts/loops.mdx`. Template: `templates/fix-until-green`.
