# Step reference — `run:` steps and their exit codes

A `run:` step executes a shell command and produces a fixed output:

| field      | type    | meaning                                                   |
|------------|---------|-----------------------------------------------------------|
| `ok`       | boolean | exit code was 0, or a code declared in `exit-codes`        |
| `exitCode` | integer | the command's exit code                                    |
| `outcome`  | string  | the `exit-codes` name of this exit (only when declared)    |
| `stdout`   | string  | trimmed standard output                                    |
| `stderr`   | string  | trimmed standard error                                     |

## A non-zero exit fails the step

By default any non-zero exit fails the step and the run. That is right for a
build or a test suite, and wrong for a command whose exit code is an *answer*.

## `exit-codes:` — an exit code is an answer, not a crash

Watches, linters and health checks often say "I found something" with a
non-zero code (`exit 3` = findings). Declare those codes and name them:

```yaml
steps:
  - id: check
    run: ./watch.sh
    exit-codes: { 0: ok, 3: findings }

  - id: triage
    needs: [check]
    when: [{ path: check.outcome, equals: findings }]
    prompt: Triage what the watch found …
```

- A declared code finishes the step as **done** with `ok: true`; the run continues.
- `exitCode` and the declared name (`outcome`) are on the output, so `when:`
  can branch on either: `check.outcome = findings` or `check.exitCode = 3`.
- An **undeclared** non-zero exit still fails the step (exit 2 above is a crash).
- Validated when the workflow is applied: codes must be 0-255, names non-empty,
  and `exit-codes` is only allowed on a `run:` step.

## The other opt-outs

- `continue-on-error: true` — every exit finishes the step as done. Coarser
  than `exit-codes`: a real crash then looks like an answer.
- A `when:` on another step that reads `<step>.exitCode` implies
  `continue-on-error` on that step.
- A gate whose condition matches the output routes instead of failing.

Prefer `exit-codes` when you know which codes are answers.

## A module command marked `effect: write` always pauses the run for approval

A step that uses a module command marked `effect: write` (sending a message,
creating an issue, writing a file, …) pauses the run for approval before any
of that command's steps runs.
- **Once per call:** the pause sits on the command's first step. A later
  approval step of the command's own, such as a response policy of
  `approval`, no longer asks again.
- **The command's own policy still applies after approval:** gh's and apl's
  default `responsePolicy: draft` still writes nothing.
- **No opt-out:** there is no way to skip this pause.

## `WFX_*` variables belong to the platform

A workflow, a job or a step may not set a `WFX_*` variable in its `env:`.
Those names are provided by the platform (`WFX_RUN_ID`, `WFX_STEP_ID`,
`WFX_API`, `WFX_DATA_*`, …) or by the env store (`wfx env set WFX_…`). A
workflow that sets one does not load, and the error names the workflow or
step and the variable. When a step runs, the env store's and the platform's
`WFX_*` values win over anything else.
