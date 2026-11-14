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

## Secrets travel through the environment, never through step output

A step gets its secrets as environment variables: env-store values, and any
credential-shaped name (`*_TOKEN`, `*_KEY`, …). If a step prints one, the
platform removes it **before anything is stored**: stdout and stderr, the
step's output and raw text, its error, the run's outcome, events, artifacts,
a paused step's question (and what notifiers send of it), and a worker's
report and live log. Text is scrubbed before it is shortened, and where a
worker shortened it first, a piece of a value left at the cut is removed too.

What is matched:
- the value itself, and with CRLF line endings when it spans lines (a PEM key);
- base64 (standard and URL alphabets, padded or not) at any byte offset, so a
  Basic-auth header (`curl -u user:secret`) is caught whatever the user name;
  also when the base64 is wrapped across lines (a PEM body, `base64` at 76
  columns) and the value crosses a line break;
- hex (either case) and URL-encoded (query and path) forms;
- inside JSON: every JSON string is decoded before it is scrubbed, so any
  escape spelling is caught (`\/` as PHP writes it, `\uXXXX` as Python's
  `json.dumps` or `jq --ascii-output` write it, `&` for `&`). A string
  whose text is itself a JSON document (a step that printed JSON) is decoded
  and scrubbed the same way, up to 4 levels deep;
- in text that is not a JSON document (a log line), each of the forms above
  JSON-escaped once, with and without `& < >` escaped.

In its place, stored text shows `[redacted:NAME]`.

The step after it therefore reads the scrubbed text: `.Steps.x.output` never
carries a secret. To hand a credential to a later step, give that step the
same environment variable; don't print it and read it back.

Limits (not caught):
- Values shorter than 6 characters are never redacted (too many false hits).
- A value split across lines (other than wrapped base64) or across separate
  strings, e.g. printed in two halves.
- A value printed as a JSON number, or numeric text a tool reformats.
- JSON nested deeper than 4 levels of JSON-in-a-string: below that, strings
  are scrubbed as text only, so an escaped spelling there survives.
- A JSON fragment inside a log line (not a whole document) is only matched in
  the common escape spellings, not `\/` or `\uXXXX`.
- The marker names the variable. Whoever can get a value printed can see that
  it matched; don't let outside input choose what a step prints.
- `wfx state set` values are stored as given, not scrubbed: never put a
  secret in state.
- A value a worker resolves on its own machine never reaches the platform, so
  the platform cannot scrub it from that worker's report.
