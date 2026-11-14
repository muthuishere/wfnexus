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

## Browser steps — a named profile per run

A machine that runs steps can hold several named browser profiles in a local
file, delivered as a carry file for example: `$WFX_BROWSER_CONFIG`, default
`~/.config/wfx/browser.json`.

```json
{"default": "us",
 "profiles": {
   "us": {"command": ["some-browser-cli"], "folder": "/data/profiles/us", "proxy": "http://127.0.0.1:8881"},
   "de": {"command": "some-browser-cli --json", "folder": "/data/profiles/de"}}}
```

- `command` is an argv array, or a string split on spaces. It can be any
  browser tool that reads its profile folder and proxy from the environment.
- `proxy` is optional. `wfx doctor` lists the profiles and says which are
  usable: the command is found and the folder is present.

The core `browser` module runs that command with one profile:

```yaml
- id: look
  uses: browser.read@0
  with: { profile: us, action: open, args: '["https://example.com"]' }
```

- The command runs with no shell as `command… <action> <args…>`. `args` is a
  JSON array string, or one argument.
- The run gets `WFX_BROWSER_PROFILE`, `WFX_BROWSER_PROFILE_DIR` and
  `WFX_BROWSER_PROXY`, plus `HTTP_PROXY` and `HTTPS_PROXY` when the profile has
  a proxy.
- Leaving `profile` empty uses the default. A profile that is named but not
  configured fails the step, and the default is never used in its place.
- `browser.act` is for actions that change something on a site. It is marked
  as a write, so the step pauses for approval.
