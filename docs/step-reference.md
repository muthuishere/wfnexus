# Step reference — `run:` steps and their exit codes

A `run:` step executes a shell command and produces a fixed output:

| field      | type    | meaning                                                   |
|------------|---------|-----------------------------------------------------------|
| `ok`       | boolean | exit code was 0, or a code declared in `exit-codes`        |
| `exitCode` | integer | the command's exit code                                    |
| `outcome`  | string  | the `exit-codes` name of this exit (only when declared)    |
| `stdout`   | string  | trimmed standard output                                    |
| `stderr`   | string  | trimmed standard error                                     |

## `run:` text may only reference values; logic goes in the script

In a `run:` step, `{{ … }}` may hold exactly one thing: a reference to a value.
A reference is a field path on the root dot:

- `{{ .Input.name }}`, `{{ .Steps.collect.output.y }}`, `{{ .Item.z }}`,
  `{{ .Decide.q }}`, `{{ .Output.v }}`, `{{ .Memory }}`;
- a state key: `{{ .Workflow.last_sha }}`, `{{ .Global.tier }}`;
- the run's own facts: `{{ .RunID }}`, `{{ .Index }}`, `{{ .WorkDir }}`, `{{ .BaseRef }}`.

Trim markers (`{{- … -}}`) and comments (`{{/* … */}}`) are fine.

**Everything else is refused when the workflow loads.** The error names the
workflow, the step and the construct. That covers `if`, `else`, `range`,
`with`, `define`, `template`, `block`, `break`, `continue`, variables (`$`,
`$x`, `:=`, `=`), pipes, function calls (`printf`, `len`, `index`, `eq`,
`json`, `default`, `or`, …), parentheses and literals. Do the logic in the
script, working on the value it receives:

```yaml
# before (refused now)
run: |
  ./deliver.py --subject "Daily brief {{ if .Input.day }}{{ .Input.day }}{{ else }}$(date +%F){{ end }}"
# after
run: |
  day="{{ .Input.day }}"
  ./deliver.py --subject "Daily brief ${day:-$(date +%F)}"
```

Prompts, gate messages, `send:` text and every other non-shell text keep full
templates.

### How a value arrives

Each reference becomes an environment variable when the step runs:
`${WFX_DATA_1}`, `${WFX_DATA_2}`, … in the order they appear, or
`${env:WFX_DATA_1}` under `pwsh`. The value travels beside the command. So a
value arrives unchanged, whatever quotes, newlines or shell syntax it holds,
and the run page shows the references, not the values. The run's own facts
arrive the same way; there is no exception for them.

- **A string arrives as it is. Any other value arrives as JSON**: an object, a
  list, a number or a boolean, indented as the `json` function writes it. An
  absent value arrives empty, whatever the path goes through: a step that did
  not run, a key a value does not have, an unset judge answer or state key. A key that only `index` could reach (a space or
  odd characters in it) can't be referenced. Reference the whole value instead
  and read the key in the script:
  `printf '%s' "{{ .Steps.x.output }}" | jq -r '.["odd key"]'`.
- **A reference sits inside double quotes or an unquoted heredoc, nowhere
  else.** Write `"{{ .Input.x }}"`, or put it in a `<<EOF` body. A workflow
  that breaks this does not load:
  - a bare word (`echo {{ .Input.x }}`, `X={{ .Input.x }}`): unquoted, the
    shell splits it on spaces, expands globs in it, and a value starting with
    `-` becomes an option;
  - single quotes (`'{{ .Input.x }}'`) or a quoted heredoc (`<<'EOF'`): they
    expand nothing, so the script would see a variable name, not the value.
- **A command that is itself an input** (a test command, say) is run on
  purpose: write it to a file as data, then run the file:
  `f=$(mktemp); printf '%s\n' "{{ .Input.test_command }}" > "$f"; bash "$f"`.
- **Places that evaluate text again are refused at load:**
  - shell arithmetic: `$(( ))`, `(( ))`, `let`, `declare`/`typeset`/`local -i`,
    a numeric `[[ … -eq … ]]`, an array subscript;
  - text given to `eval`, to `trap`, or as the script of a shell's `-c`
    (`sh`, `bash`, `zsh`, `dash`, `ksh`, `csh`, `tcsh`, `fish`, `"$BASH"`,
    `$SHELL`; also glued `sh -c"…"`, after options such as `-o pipefail`, and
    after `--`), `su`/`runuser … -c`, `ssh`, or pwsh
    `Invoke-Expression`/`iex`. Quoted or escaped command words (`"sh"`,
    `\eval`) count the same;
  - the code argument of an interpreter: `python -c`, `node -e`/`-p`,
    `perl -e`, `ruby -e`, `php -r`, and pwsh `-Command`;
  - text a program reads on stdin **as its script**: a pipe, heredoc,
    here-string or `<( … )` feeding a shell or interpreter that has no script
    file and no code argument (`| sh`, `| python3`, `| bash -s`,
    `python3 - <<EOF`, `. /dev/stdin <<<"…"`). Given a script or code, what it
    reads on stdin is data. These are fine:
    `echo "{{ .Input.x }}" | python3 -c '…json.load(sys.stdin)…'`,
    `| node -e '…'`, `| perl -ne '…'`, `| python3 parse.py`,
    `| bash ./consume.sh`, `| ssh host 'cat > f'`, `python3 analyze.py <<EOF`.

  For a number, read it into a variable, check it
  (`case "$n" in ''|*[!0-9]*) exit 2;; esac`), then use the variable. For
  `sh -c`, pass the value as an argument: `sh -c 'echo "$1"' _ "{{ .Input.x }}"`.
  For an interpreter, pass it after the code:
  `python3 -c 'import sys; print(sys.argv[1])' "{{ .Input.x }}"`.
- **The shell checks are a guard, not a proof.** They catch the common ways
  a value ends up evaluated, not every way. Not detected:
  - a value used as a variable name (`test -v`, `printf -v`, `read`,
    `${!…}`);
  - a value copied into a variable and then used in arithmetic;
  - a value written to a file that is then sourced or run;
  - a value inside the program text of `awk`, `sed -e`, `jq`, `find -exec`,
    `watch`, `flock -c`, `script -c`, `tmux` commands, `git -c alias.*=!…`, or
    an interpreter not listed above;
  - a command word split by a line continuation (`ev\` + newline + `al`), or
    named by a default (`${SHELL:-sh} -c`);
  - a shell started through a wrapper's own placeholder (`xargs -I{} sh -c`);
  - a value in `PS4` with `set -x`, or in an `alias`;
  - pwsh `[scriptblock]::Create(…)`.

  Pass values to such programs as arguments, environment or files, never inside
  their code. For input that comes from outside (hooks, MCP), typed inputs that
  limit what a value may be (#137) are the defence; this check is not.
- **Large values go to a file.** A value over 64 KiB (or once a step's values
  pass 256 KiB together, or one holding a NUL byte) is written to a 0600 file.
  `WFX_DATA_<n>_FILE` names it, and the reference becomes
  `$(cat "${WFX_DATA_<n>_FILE}")`. Read that way, a value is not exact in two
  cases: command substitution drops trailing newlines, and the shell drops a
  NUL byte. The file itself holds the value exactly, so a script that needs
  every byte reads the file.
- **`shell: cmd` cannot take values**: cmd re-reads an expanded variable as
  code. Use `pwsh`, `bash` or `sh`.
- The same happens on a worker: the job carries the command and its values
  separately, and the worker uses its own shell's syntax for the references.
- A task's or module's literal inputs (`inputs:`/`with:` values) become the
  step's constants, referenced as `{{ .Const.<name> }}` by the input's own
  name, and arrive the same way.

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
- **Where the pause sits:** on the command's first step, before anything of
  that call has run. A step the module or the workflow itself marks
  `requires_approval` keeps that as well.
- **What the person sees:** the pause shows what the call will run, every
  step of it with its inputs filled in, scrubbed of the run's secrets.
- **Pinned:** the run holds what was approved. Its fingerprint covers each
  step's script, every value the script receives (`WFX_DATA_n`, and the files
  large values go to), the step's constants, the environment the call runs
  with (hashed, never shown: `GH_HOST`, env-store values, …), the module's
  version, the step's definition and the module profile the call resolves
  to. It is checked before every step of the call, not only the first: if any
  of it changes (a workflow edited and reloaded, a value, an env-store entry
  or the profile changed), the step fails with "changed after it was
  approved" instead of running something nobody approved. Approve it again
  with a retry.
- **The profile is resolved at the pause:** a command with a `profile` input
  (gh's repo, apl's handles) gets the profile the call names, or the module's
  default, resolved by the engine when the run pauses. The pause shows it
  (name, account, its inputs such as `repo=`), and every step of the call
  runs with that resolved profile (`WFX_MODULE_PROFILE`); a step never looks
  the default up for itself. Where the server resolves the profile again (apl
  `whatsapp-send` queues on the service's outbox), the step hands it the
  approved account, and a profile that names another account by then is
  refused: nothing is queued.
- **What the pin does not cover: which account a CLI is logged in as.** The
  pin covers the call's inputs, environment and profile. The identity behind
  the tools a module runs (`gh auth login`, an apl login, a cloud CLI's
  credentials) is the machine's, outside the run: switching that login while
  a run waits changes who the approved write is sent as. Keep those logins
  stable on a server that runs writes, or give each its own machine.
- **A retry starts the call again:** retrying any step of a write call resets
  the call from its first step, which pauses again for approval.
- **Files between a call's steps:** after each step of a write call the
  engine fingerprints the call's files (`in.*`, `plan.json`). The next write
  step compares before it runs: a file changed in between, by anything,
  fails it with "changed after it was approved". The record (and the call's
  approval record, once its first step has started) is held in memory, so
  after a server restart a half-done write call fails (nothing more is
  written); run it again.
- **A call follows its first step:** a call's `when:` is decided once, on
  the call's first step. If that step is skipped, every step of the call is
  skipped. A write step runs only after the call's first step was approved
  and finished, whatever else has run in between.
- **The command's own policy still applies after approval:** gh's and apl's
  default `responsePolicy: draft` still writes nothing, and `none` skips.
  `approval` = the engine's pause: the call has already waited for a person,
  so it then writes, as `auto` does, with no second pause.
- **No opt-out:** a `with:` override that sets `requires_approval: false` on
  a module command's step is refused at load, at any level of `uses:`. Other
  overrides leave the pause in place.
- **Not in a planned workflow:** a workflow ordered by `goal:` or
  `consumes`/`produces` cannot use a write command; it is refused at load,
  naming the workflow and step. (The planner honours each step's `needs`, so
  a module's steps keep their order there.)
- **No loops on a `uses:` step:** `foreach`, `until` and `while` on it are a
  load error. Move the loop into the called workflow, or call the command once
  per run.
- **Who may approve:** if the workflow has an approvers policy (`approvers:`
  or `prevent_self_approval` on any step), the person who started the run may
  not approve its own write. With no policy, as on a one-person install, the
  starter may approve.
  - A run started by another run counts as started by the person at the top.
  - The policy is the one in force when the run paused; a reloaded
    definition doesn't change it.
  - A write's approval needs a named approver; there is no exemption for an
    unnamed local answer.
  - Under a policy, a write's approval needs a signed-in user. On the
    unauthenticated loopback the approver is only a typed name, and anyone
    there could type an approver's name (or any name but the starter's), so a
    typed name is refused for a write held to a policy. With no policy a
    typed name is still accepted.
  - The loopback device sign-in (`/api/device/verify` with no session)
    creates only the install's FIRST account, its superadmin owner, and
    signs loopback-only accounts in again later. Any other new name is
    refused (an admin adds users: `wfx user add`), so a name typed at the
    machine can never become a new account with a role. That is what an
    approver policy on loopback rests on. It also refuses an account with a
    password (sign in with it: `wfx login --user NAME`) and one an external
    provider signs in (`name@provider`).
  - A loopback-only account (no password, no external sign-in) has no
    credential beyond the machine, so anyone at the machine can sign in as
    it. Give approvers a password or an external sign-in for an approver
    policy to hold on loopback.
  - A second account or a service token counts as a different person: the
    check is on the account name, not on who is behind it.
- **Agents are different:** an agent step that lists a write command in its
  `tools:` gets only the low-confidence pause (`min_confidence`), not this
  mandatory one. Give a write to a `uses:` step when it must always be
  approved.
- **Each call has its own directory:** a module command's steps run in a
  directory of their own under the run's directory (not the checkout), so two
  calls running at once don't share their `in.*` files. That is housekeeping,
  not a boundary: a step runs as the same user and can reach it. What guards
  a write is the fingerprint above. The directory is left out of the run's
  archive, and a worker removes it after the call's last step. The checkout
  is in `WFX_WORKSPACE`.
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
(`needs_input`, `awaiting_approval`) or, when a step was still in flight
then (a sibling in the same wave), after that step finishes, so a late write (a cancelled step, a
parallel branch, a worker's report, the pause's notification) is still
scrubbed; at most 256 ended or parked runs' values are held, the oldest
dropped first, never one with a step in flight. A parked run that resumes registers its values again when its
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
