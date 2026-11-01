## ADDED Requirements

### Requirement: A step can repeat once per item of a list

The system SHALL accept `foreach:` on a step as a path (`input.<key>` or `<step>.<field>`) or a literal
list, and SHALL run the step — or its `steps:` body — once per item, exposing the item as `.Item` and its
0-based position as `.Index` to templates. `parallel: N` SHALL cap concurrent iterations; absent or 1
runs them one at a time in order.

#### Scenario: foreach over an input list

- **WHEN** a run's input has `files: [a.go, b.go, c.go]` and a step declares `foreach: input.files`
- **THEN** three iteration rows `review[0]`, `review[1]`, `review[2]` run, each rendering its own item

#### Scenario: foreach over an earlier step's array

- **WHEN** a step declares `foreach: list.services` and `list`'s output schema declares `services` as an array
- **THEN** the loop iterates over the array `list` actually returned

#### Scenario: A foreach path that cannot resolve is refused at load

- **WHEN** a foreach path names an undeclared input, an unknown step, a field that is not an array, a step
  that runs after the loop, or is written as a template
- **THEN** the workflow does not load and the error names the path

### Requirement: Repeat-until loops are always bounded

The system SHALL accept `until:` and `while:` guards (the `when` shape) checked after each iteration, and
SHALL refuse either without `max_iterations` between 1 and 999. A guard SHALL read something the loop
produces. A loop that spends `max_iterations` without its condition settling SHALL fail the loop step and
the run, naming the last check.

#### Scenario: Until a check passes

- **WHEN** a loop body `fix` → `check` declares `until: [{path: check.ok, equals: true}]` and `check`
  passes on the third iteration
- **THEN** the loop finishes done after three iterations and the next step runs

#### Scenario: An unbounded loop is refused

- **WHEN** a step declares `until` or `while` without `max_iterations`
- **THEN** the workflow does not load

#### Scenario: Exhaustion fails

- **WHEN** a loop reaches `max_iterations` and its until condition still does not hold
- **THEN** the loop step and the run are failed with "until condition not met after N iterations"

### Requirement: Every iteration is persisted and visible

The system SHALL record every iteration as its own step row with status, prompt, output and timing, and
SHALL aggregate the loop step's output as `{iterations, count, last}` with `iterations` in item order.

#### Scenario: Aggregated output is readable downstream

- **WHEN** a later step renders `{{ .Steps.review.count }}`
- **THEN** it reads the number of iterations that ran

#### Scenario: Resume replays finished iterations

- **WHEN** a run paused inside a loop resumes
- **THEN** iterations whose rows are done are not re-run and the loop continues from the first that is not

### Requirement: Human gates work inside loops

The system SHALL pause for `requires_approval` per iteration on a step that repeats itself, once before
the loop on a loop with a body, and per iteration on a body step, and SHALL enforce `approvers` and
`prevent_self_approval` on an iteration as on its step.

#### Scenario: Approval per iteration

- **WHEN** a step with `requires_approval: true` repeats over two items
- **THEN** the run pauses at `deploy[0]`, and after it is approved pauses at `deploy[1]`, and finishes after
  the second approval

### Requirement: The dry run shows the planned iterations

The system SHALL report, for each loop step, its mode, the iteration row ids it would create (all of them
for a foreach over known input; up to `max_iterations` for until/while), and SHALL check each body step's
templates as the first iteration would render them.

#### Scenario: Dry run of an until loop

- **WHEN** a workflow with a two-step body and `max_iterations: 4` is dry-run
- **THEN** the loop lists eight planned row ids and each body step's rendered template
