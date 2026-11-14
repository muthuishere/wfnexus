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
   "us": {"command": ["some-browser-cli"], "folder": "/data/profiles/us", "proxy": "http://127.0.0.1:8881",
          "read_actions": ["open", "snapshot", "text"]},
   "de": {"command": "some-browser-cli --json", "folder": "/data/profiles/de"}}}
```

- `command` is an argv array, or a string split on spaces. It can be any
  browser tool that reads its profile folder and proxy from the environment.
- `proxy` is optional. It must not carry credentials (`user@` or
  `user:pass@`): such a profile is refused. Point it at a local proxy instead.
- `read_actions` lists the actions `browser.read` may run with this profile.
  If no list is given, the profile allows no reads.
  Only the action word is checked; its arguments are not. List only actions that
  cannot change a site whatever their arguments; anything that can submit, post
  or run a scripted sequence belongs in `browser.act`.
- `wfx doctor` lists the profiles with their read actions and says which are
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
- `browser.read` refuses any action the profile does not list in
  `read_actions` (exit 2), before the command runs.
- `browser.act` runs any action. It is marked `effect: write`, so the engine
  pauses the run for approval before it, every time (see below). `browser.read`
  is not a write and does not pause.

## A module command marked `effect: write` always pauses the run for approval

A step that uses a module command marked `effect: write` (sending a message,
creating an issue, writing a file, …) pauses the run for approval before any
of that command's steps runs.
- **Where the pause sits:** on the command's first step, before anything of
  that call has run. A step the module itself marks `requires_approval` keeps
  that as well. For example, gh's and apl's `responsePolicy: approval` asks
  again before the write.
- **What the person sees:** the pause shows what the call will run, every
  step of it with its inputs filled in, scrubbed of the run's secrets.
- **Pinned:** the run holds what was approved. If that text changes before
  the step starts (the workflow is edited and reloaded, or a value it reads
  changes), the step fails ("changed after it was approved") instead of
  running something nobody approved. Approve it again with a retry.
- **The command's own policy still applies after approval:** gh's and apl's
  default `responsePolicy: draft` still writes nothing.
- **No opt-out:** a `with:` override that sets `requires_approval: false` on
  a module command's step is refused at load, at any level of `uses:`. Other
  overrides leave the pause in place.
- **Not in a planned workflow:** a workflow ordered by `goal:` or
  `consumes`/`produces` cannot use a write command; it is refused at load,
  naming the workflow and step. (The planner honours each step's `needs`, so
  a module's steps keep their order there.)
- **No loops on a `uses:` step:** `foreach`, `until` and `while` on it are a
  load error. Put the call in a loop's `steps:` instead.
- **Who may approve:** if the workflow has an approvers policy (`approvers:`
  or `prevent_self_approval` on any step), the person who started the run may
  not approve its own write. With no policy, as on a one-person install, the
  starter may approve.
- **Agents are different:** an agent step that lists a write command in its
  `tools:` gets only the low-confidence pause (`min_confidence`), not this
  mandatory one. Give a write to a `uses:` step when it must always be
  approved.
- **Each call has its own directory:** a module command's steps run in a
  directory of their own under the run's directory (not the checkout). Two
  calls running at once never share their `in.*` files, and nothing there is
  ever committed. The checkout is in `WFX_WORKSPACE`.
- **gh:** `gh.api` only reads (GET); any other method is refused with exit 2.
  Writing through the API is `gh.api-write` (a write: it pauses), which
  needs a method other than GET.
- **Inputs:** module input handling (values reaching module scripts as data)
  is fixed by #136, so merge #136 first.

## `WFX_*` variables belong to the platform

A workflow, a job or a step may not set a `WFX_*` variable in its `env:`.
Those names are provided by the platform (`WFX_RUN_ID`, `WFX_STEP_ID`,
`WFX_API`, `WFX_DATA_*`, …) or by the env store (`wfx env set WFX_…`). A
workflow that sets one does not load, and the error names the workflow or
step and the variable. When a step runs, the env store's and the platform's
`WFX_*` values win over anything else.
