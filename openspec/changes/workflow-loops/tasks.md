## 1. The YAML

- [x] 1.1 `Step` gains `foreach`, `parallel`, `until`, `while`, `max_iterations`, `steps`; `TemplateData`
      gains `Item`, `Index`. Both dialects (yaml snake_case, API camelCase) decode them.
- [x] 1.2 `normalize` normalises a loop body like a workflow of its own (env/runs-on inherited) and marks
      steps whose result a loop guard reads as `continue-on-error`.
- [x] 1.3 `validateLoop`: one mode; until/while need `max_iterations` (1..999); guards need path and
      equals/exists and must read something the loop produces; foreach path resolves to a declared array
      that runs earlier (or is needed, in a DAG); no templates in foreach; no nested loops; no
      needs/consumes/produces/skip_to in a body; body ids unique and not shadowing outer ids; `[`/`]`
      refused in ids. A body is then validated like a workflow.
- [x] 1.4 Verification: `internal/workflow/loop_test.go` — accepted shapes and every rejection.

## 2. The engine

- [x] 2.1 `runOneStep` dispatches loop steps to `runLoop` (engine/loop.go); sequential, DAG and planned
      paths all get loops from that one place.
- [x] 2.2 Each iteration is a step row (`IterationID`, `IterationPosition`) run by `runOneStep`; done rows
      replay on resume; foreach rows are created up front, until/while rows per iteration.
- [x] 2.3 foreach with `parallel` cap, results in item order; until/while sequential with the previous
      iteration's outputs visible; exhaustion fails with the last check.
- [x] 2.4 Aggregate output `{iterations, count, last}`; `ResultSchema` exposes it to dry-run reference
      checks and sample outputs.
- [x] 2.5 Approvals: single-step loop per iteration, body loop once, body step per iteration;
      `Definition.Lookup` resolves iteration rows for `approvers`.
- [x] 2.6 Retry resets by range (`ResetStepsRange`) so a finished loop keeps its record.
- [x] 2.7 Replays skip iteration rows.
- [x] 2.8 Verification (Postgres via pgtest): `internal/engine/loop_test.go` — foreach over input with
      parallel; until over a body with previous-iteration visibility; exhaustion fails; approval per
      iteration and resume; DAG foreach over a step's array; store range reset (sqlite).

## 3. Dry run

- [x] 3.1 `DryRunStep.Loop` — mode, items (known for input lists), planned iteration ids, until text,
      body steps checked as iteration 0.
- [x] 3.2 `.Item`/`.Index` in a non-loop step is flagged.
- [x] 3.3 Verification: `TestDryRunShowsPlannedIterations`, `TestFixUntilGreenTemplateDryRuns`.

## 4. UI, docs, example

- [x] 4.1 `api.ts` Step/Guard types; run page lists iteration rows under their loop (`lib/loops.ts`).
- [x] 4.2 Builder validator mirrors the loop checks; `loops.test.ts`.
- [x] 4.3 `site/src/content/docs/concepts/loops.mdx` + sidebar + workflows-and-steps key table.
- [x] 4.4 `templates/fix-until-green` — a bounded repair loop with a human gate.

## 5. Open

- [ ] 5.1 `dryCatalog`/`dryRequirements`/`dryState` do not yet walk body steps.
- [ ] 5.2 Step-scope state inside an iteration is addressed by the iteration row id.
- [ ] 5.3 Builder canvas does not yet edit a loop body visually (the YAML editor does).
