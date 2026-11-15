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
a paused step's question (and what notifiers send of it), what it writes to
`$WFX_STATE` and a `state:` value (kept beyond the run), a `send:` step's
text and file attachment, and a worker's report and live log (also after a
server restart). Whenever the platform shortens a text, the whole text is
scrubbed first and then cut, so no piece of a value is left at the cut. A
local agent CLI's rejected reply, quoted in the step's error by its two
ends, is scrubbed whole before it is shortened too.

What is matched:
- the value itself, and with CRLF line endings when it spans lines (a PEM key);
- base64 (standard and URL alphabets, padded or not), also inside a longer
  encoded run: at each of the three byte offsets, with or without bytes
  after it, every character that encodes the value's bytes alone is
  replaced, so a Basic-auth header (`curl -u user:secret`, `curl -u
  "$KEY:"`) is caught whatever the user name. Up to one character at each
  edge is left in place: at the start, the one that also encodes the byte
  before the value; at the end, the one that also encodes the byte after
  it, or, when nothing follows the value and it does not end on a 3-byte
  boundary, the last character, which holds at most 4 bits of the value
  (`lG[redacted:B64]Q==`). Also caught
  when the base64 is wrapped across lines (a PEM body, `base64` at 76
  columns) and the value crosses a line break;
- hex (all lower or all upper case);
- percent-encoding in any case (`%2f` or `%2F`), whichever bytes are
  encoded: Go's and Python's query and path escaping, JavaScript's
  `encodeURIComponent`, and every byte encoded;
- inside JSON: every JSON string is decoded before it is scrubbed, so any
  escape spelling is caught (`\/` as PHP writes it, `\uXXXX` as Python's
  `json.dumps` or `jq --ascii-output` write it, `\u0026` for `&`). A string
  whose text is itself a JSON document (a step that printed JSON) is decoded
  and scrubbed the same way, up to 4 levels deep. Deeper than that, a JSON
  value holding a secret anywhere inside is replaced whole by
  `[redacted:nested JSON]`;
- in text that is not one JSON document — a JSON output cut to size, NDJSON,
  JSON inside a log line, JSON Go cannot parse, an error message — the text
  is also read with its JSON escapes decoded (`\/`, `\uXXXX`, `\"`, `\\`,
  `\n` and the rest) and its percent-encoding decoded, in turn, up to 4
  levels (a value escaped twice, JSON inside a JSON string in a log line);
- a value made of digits printed as a JSON number is replaced by the marker
  (as a string).

Numbers in a scrubbed JSON document keep their digits (a 20-digit id is not
rounded, 1000000 stays 1000000), and a document with nothing to redact is
stored exactly as it was.

The values are held in the server's memory while a step of the run is
running, and for 10 minutes after the run ends or parks on a person
(`needs_input`, `awaiting_approval`), so a late write (a cancelled step, a
parallel branch, a worker's report, the pause's notification) is still
scrubbed; at most 256 ended or parked runs' values are held, the oldest
dropped first. A parked run that resumes registers its values again when its
next step starts, from that step's environment: a literal value only an
earlier step's `env:` named is not known to the steps after the pause. A
worker's report or live log that arrives later than that is scrubbed with
the values loaded again from the env store and the job.

In its place, stored text shows `[redacted:NAME]`.

The step after it therefore reads the scrubbed text: `.Steps.x.output` never
carries a secret. To hand a credential to a later step, give that step the
same environment variable; don't print it and read it back.

Limits (not caught):
- Values shorter than 6 characters are never redacted (too many false hits);
  the server log warns, naming the variable, when one is registered.
- A value split across lines (other than wrapped base64) or across separate
  strings, e.g. printed in two halves.
- Numeric text a tool reformats (`1.2e7`, thousands separators).
- Other encodings: base32, hex in mixed case, HTML entities (`&#47;`,
  `&amp;`), JSON escaped more than 4 times inside plain text.
- A text cut BEFORE the platform sees it. At a worker's 64 KB cap (the
  worker holds no secrets), the piece of a value right before the
  `… truncated` marker is removed, in every form listed above (as itself,
  base64 at each byte offset, hex, percent-encoded in any case or spelling,
  JSON-escaped, both in turn), together with an escape the cut split in two
  (`%3`, `\u00`), when that piece is at least 4 characters long as
  written. A shorter piece stays: at most 3 characters, which is 3 bytes of
  the value as itself, or 1 byte when every byte is `%xx`; so does an
  escape cut before the value's first byte (`\u00`), which holds none of
  it. Before a bare `…` that ends a text (which also ends ordinary
  sentences) the piece must be 6 characters long.
- A provider error body that the model library shortens with `…` in the
  middle of its message is not treated as cut: a piece of a value there
  stays. The same holds for the last 4 KB of an agent CLI's stderr that the
  adapter keeps when the CLI dies: a value cut at the start of that window
  leaves its end.
- The marker names the variable. Whoever can get a value printed can see that
  it matched; don't let outside input choose what a step prints.
- `wfx state set` values (set from the CLI or API, not by a step) are stored
  as given, not scrubbed: never put a secret in state.
- A value a worker resolves on its own machine never reaches the platform, so
  the platform cannot scrub it from that worker's report.
