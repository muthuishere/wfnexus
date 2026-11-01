## Context

The engine has three orderings (sequential, DAG, planned) that share one per-step executor,
`runOneStep` (engine/schedule.go), which owns approval, decide, retry, gates, state and the terminal run
status. Step rows live in `step_runs` keyed by `(run_id, step_id)` and ordered by `position`.

## Goals / Non-Goals

**Goals:** foreach and until/while with GitHub-Actions/Argo-flavoured syntax; every iteration persisted
and visible; mandatory bounds; aggregate output; gates and approvals inside loops; dry-run plan.

**Non-Goals:** nested loops; `needs`/planning inside a body; templated `max_iterations`; a pre-checked
`while` (both conditions are checked after an iteration).

## Decisions

### 1. An iteration IS a step row, run by `runOneStep`

A child step is a copy of the body step (or the loop step itself) with its id rewritten to
`<loop>[<i>]` or `<loop>[<i>].<body>`. `runOneStep` runs it like any step. Alternatives considered:
storing iterations inside the parent row's output (no per-iteration status, approvals or retry), or a new
`iterations` table (a migration plus a second executor to keep in sync with the first). Reusing the row
gives approvals, `ask_human`, gates, retries, SSE events and resume for free, and is why there is no
migration.

### 2. Ids and positions

`[` and `]` are refused in step ids, so an iteration id is unambiguous. Positions are
`1_000_000 + parentPos*100_000 + i*100 + bodyIdx`: every iteration sorts after every definition step,
grouped by loop, then iteration, then body step. This bounds a loop to 999 iterations and 99 body steps.
Replays that count definition steps (sequential start index, DAG `done`, planned world) skip iteration
rows; the loop step's own row says whether the loop finished.

### 3. Retry resets by range

`ResetStepsFrom(pos)` would also wipe the iteration rows of a loop that finished before `pos`. Retry now
resets `[pos, 1_000_000)` and `[IterationBase(pos), ∞)`. Retrying an iteration resets it and every later
iteration row, plus the definition steps after its loop.

### 4. Conditions and visibility

`until`/`while` reuse `Guard` and the planner world (`guardFor`/`guardWhy`). Facts are the outer outputs,
plus this iteration's body outputs keyed by body id (or the loop step's own output under its id). The
next iteration's templates see the previous iteration's outputs, so a fix step reads the last failing
check. A step whose result a loop guard reads is marked `continue-on-error` at load, because a red check
is the reason to loop, not a failure.

### 5. Approval placement

`requires_approval` on a single-step loop is asked per iteration (each iteration is the step); on a loop
with a body it is asked once before the loop; on a body step it is asked in each iteration.
`Definition.Lookup` resolves an iteration row to its definition so `approvers` and
`prevent_self_approval` are enforced.

### 6. Exhaustion fails

A loop that spends `max_iterations` without its condition settling fails the loop row and the run with
the last `guardWhy`. A failed iteration fails the loop row too, so the run never shows a failed run with
its loop still `running`.

### 7. foreach with `parallel`

Iterations acquire a semaphore of size `parallel`; results are written by index so the aggregate is in
item order. Once an iteration halts (approval, input, failure) no new iteration starts; in-flight ones
finish, as in the DAG scheduler.

## Risks / Trade-offs

- Concurrent iterations each set the run's current step; a halted iteration's status can be overwritten
  by a still-running sibling's `running` until it finishes — the same trade-off the DAG path makes.
- State written by an iteration's `state:` block is addressed by the iteration's row id for the step
  scope. Workflow/project/global scopes are unaffected.
- Dry-run provider/env/skill checks run for body steps through the same per-step checker, but the
  catalog/requirements passes (`dryCatalog`, `dryRequirements`, `dryState`) still look only at
  top-level steps.
