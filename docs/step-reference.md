# Step reference — `run:` steps and their exit codes

A `run:` step executes a shell command and produces a fixed output:

| field      | type    | meaning                                                   |
|------------|---------|-----------------------------------------------------------|
| `ok`       | boolean | exit code was 0, or a code declared in `exit-codes`        |
| `exitCode` | integer | the command's exit code                                    |
| `outcome`  | string  | the `exit-codes` name of this exit (only when declared)    |
| `stdout`   | string  | trimmed standard output                                    |
| `stderr`   | string  | trimmed standard error                                     |

## Values in `run:` text arrive through the environment

`{{ … }}` in a `run:` step fills in a value: the run's input, an earlier step's
output, state, memory, a loop item. Each such value reaches the script as
data. When the step runs, every `{{ … }}` that prints a value becomes a
reference to an environment variable (`${WFX_DATA_1}`, `${WFX_DATA_2}`, … in
the order they appear; `${env:WFX_DATA_1}` under `pwsh`), and the value travels
in the environment. So a value arrives byte for byte, whatever quotes, newlines
or shell syntax it holds, and the run page shows the references, not the values.

```yaml
- id: greet
  run: |
    echo "hello {{ .Input.who }}"          # fine: double quotes expand it
    cat > notes.json <<EOF
    {{ json .Steps.collect.output }}
    EOF
```

What this means when you write a step:

- **Single quotes and quoted heredocs are refused.** `'{{ .Input.x }}'` or a
  heredoc with a quoted delimiter (`<<'EOF'`) would print the variable's name,
  because neither expands variables. Such a workflow does not load, and the
  error names the step. Use double quotes, a bare word, or an unquoted
  heredoc (`<<EOF`).
- **Conditions are unchanged.** `{{ if eq .Input.mode "fast" }}--fast{{ end }}`
  still branches on the value; only printed values move to the environment.
- **The run's own facts print as they are:** `.RunID`, `.WorkDir`, `.BaseRef`
  and `.Index`, so `cd '{{ .WorkDir }}'` keeps working.
- **Large values go to a file.** A value over 64 KiB (or once a step's values
  pass 256 KiB together) is written to a 0600 file. `WFX_DATA_<n>_FILE` names
  it, and the reference becomes `$(cat "${WFX_DATA_<n>_FILE}")`, which drops
  trailing newlines as command substitution does. A script can also read the
  file itself.
- **`shell: cmd` cannot take values**: cmd re-reads an expanded variable as
  code. Use `pwsh`, `bash` or `sh`.
- The same happens on a worker: the job carries the command and its values
  separately, and the worker uses its own shell's syntax for the references.

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
