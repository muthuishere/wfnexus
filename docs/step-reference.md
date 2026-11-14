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
in the environment. So a value arrives unchanged, whatever quotes, newlines or
shell syntax it holds (two exceptions under "Large values" below), and the run
page shows the references, not the values.

```yaml
- id: greet
  run: |
    echo "hello {{ .Input.who }}"          # fine: double quotes expand it
    cat > notes.json <<EOF
    {{ json .Steps.collect.output }}
    EOF
```

What this means when you write a step:

- **A value sits inside double quotes or an unquoted heredoc — nowhere else.**
  Write `"{{ .Input.x }}"` or put it in a `<<EOF` body. A workflow that breaks
  this does not load, and the error names the step:
  - a bare word (`echo {{ .Input.x }}`, `gh pr view {{ .Inputs.n }}`,
    `X={{ .Input.x }}`): unquoted, the shell splits it on spaces, expands
    globs in it, and a value starting with `-` becomes an option;
  - single quotes (`'{{ .Input.x }}'`) or a quoted heredoc (`<<'EOF'`): they
    expand nothing, so the script would see a variable name, not the value.
- **A command that is itself an input** (a test command, say) is run on
  purpose: write it to a file as data, then run the file —
  `f=$(mktemp); printf '%s\n' "{{ .Input.test_command }}" > "$f"; bash "$f"`.
- **Places that evaluate text again are refused at load:**
  - shell arithmetic: `$(( ))`, `(( ))`, `let`, `declare`/`typeset`/`local -i`,
    a numeric `[[ … -eq … ]]`, an array subscript;
  - text given to `eval`, `trap`, the script of a shell's `-c`
    (`sh`, `bash`, `zsh`, `dash`, `ksh`, `csh`, `tcsh`, `fish`, `"$BASH"`,
    `$SHELL`; also glued `sh -c"…"`, after options such as `-o pipefail`, and
    after `--`), `su`/`runuser … -c`, `ssh`, or pwsh
    `Invoke-Expression`/`iex`. Quoted or escaped command words (`"sh"`,
    `\eval`) count the same;
  - the code argument of an interpreter: `python -c`, `node -e`/`-p`,
    `perl -e`, `ruby -e`, `php -r`, and pwsh `-Command`;
  - text fed to a shell or interpreter as its script: a heredoc that `sh`,
    `bash`, `python`, `node`, `perl`, `ruby` and the like read
    (`bash <<EOF`, also after a line continuation; `cat <<EOF | sh`), a pipe
    into one (`echo "…" | sh`, `|& bash`, a pipe continued on the next line,
    inside `$( … )`, or through `env -u X`, `sudo -u u`, `xargs -I {}` and
    similar), a here-string (`bash <<<"…"`, `. /dev/stdin <<<"…"`) or `<( … )`.

  For a number, read it into a variable, check it
  (`case "$n" in ''|*[!0-9]*) exit 2;; esac`), then use the variable. For
  `sh -c`, pass the value as an argument: `sh -c 'echo "$1"' _ "{{ .Input.x }}"`;
  for an interpreter, after the code:
  `python3 -c 'import sys; print(sys.argv[1])' "{{ .Input.x }}"`.
  For a script read on stdin, put the value in the environment and quote the
  heredoc: `V="{{ .Input.x }}" bash <<'EOF'` … `echo "$V"` … `EOF`.
- **This check is a guard, not a proof.** It catches the common ways a value
  ends up evaluated, not every way. Not detected:
  - a value used as a variable name (`test -v`, `printf -v`, `read`,
    `${!…}`);
  - a value copied into a variable and then used in arithmetic;
  - a value written to a file that is then sourced or run;
  - a value inside the program text of `awk`, `sed -e`, `jq`, `find -exec` or an
    interpreter not listed above.

  Pass values to such programs as arguments, environment or files, never inside
  their code. For input that comes from outside (hooks, MCP), typed inputs that
  limit what a value may be (#137) are the defence; this check is not.
- **`define`, `template` and `block` are not used in `run:` text** (they are
  refused at load); write the text in place.
- **`$` is never declared or assigned in `run:` text** (`{{ $ = … }}`,
  `{{ $ := … }}`, `range $ := …`, `with $ := …`, anywhere): refused at load.
  Use a variable of another name.
- **Conditions are unchanged.** `{{ if eq .Input.mode "fast" }}--fast{{ end }}`
  still branches on the value; only printed values move to the environment.
- **Only `.RunID` and `.Index` print as text.** The engine makes them (a UUID
  and an integer), so they cannot hold shell syntax — and only where the dot is
  the run's own data: at the top level, or written `$.RunID` anywhere. Inside
  `range` or `with` the dot is a value, so `.RunID` there is that value's field
  and travels as data. **Everything else is data, `.WorkDir` and `.BaseRef`
  included** (a working folder can be a path the caller gave): write
  `cd "{{ .WorkDir }}"` and `git log "{{ .BaseRef }}..HEAD"`.
- **Large values go to a file.** A value over 64 KiB (or once a step's values
  pass 256 KiB together, or one holding a NUL byte) is written to a 0600 file. `WFX_DATA_<n>_FILE` names
  it, and the reference becomes `$(cat "${WFX_DATA_<n>_FILE}")`. Read that
  way, a value is not exact in two cases: command substitution drops trailing
  newlines, and the shell drops a NUL byte. The file itself holds the value
  exactly, so a script that needs every byte reads the file.
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

## The `conversation` module: tasks on an external conversational service

`conversation.start_task` asks an external conversational service to run a
task: contact these targets on this channel and collect these fields.
`conversation.task_status` reads the task back. The template
`templates/conversation-task` does both.

```yaml
steps:
  - id: start
    uses: conversation.start_task@0          # effect: write, so the run pauses for approval first
    with:
      task_type: collect
      targets: '["+15550100", {"id": "c-42", "phone": "+15550101", "name": "Ada"}]'
      fields: '["name", "email"]'
      channel: whatsapp
      service_url_env: WFX_CONVERSATION_URL      # NAMES of variables, not values
      api_key_env: WFX_CONVERSATION_KEY
  - id: wait
    uses: conversation.task_status@0
    with:
      task_id: '{{ (fromJson .Steps.start.start.stdout).task_id }}'
      wait_seconds: "1800"                   # 0 = read once
      poll_seconds: "30"
      service_url_env: WFX_CONVERSATION_URL
      api_key_env: WFX_CONVERSATION_KEY
```

- **Credentials:** `service_url_env` and `api_key_env` name environment
  variables (set with `wfx env`, the key with `--secret`).
  - Only names that start with `WFX_CONVERSATION_` are accepted (for example
    `WFX_CONVERSATION_URL`, `WFX_CONVERSATION_KEY`,
    `WFX_CONVERSATION_SUPPORT_KEY`). Any other name is refused before the
    environment is read, so a step can't send another of the machine's
    credentials.
  - Neither value is written into a workflow file, and the key is never
    printed.
  - Once steps read sec directly (#128), these become sec names.
- **The URL:** it must be https; plain http is allowed only for localhost,
  127.0.0.0/8 and ::1. Redirects are never followed, so the key never goes to
  another origin.
- **The request:** `start_task` sends `POST {url}/tasks` with
  `{type, targets, fields, channel}`, `Authorization: Bearer <key>` and
  `Idempotency-Key: <run id>:<step id>`. A retried step therefore starts no
  second task. Inside a loop the step id names the iteration (`each[1].…`),
  so each iteration starts its own task. Output: `{task_id, status}`.
- **Approval:** `start_task` is marked `effect: write`. The engine therefore
  pauses the run for approval before it, every time: it starts contacting
  people.
- **Polling:** `task_status` sends `GET {url}/tasks/{id}` until the status is
  `done`, `failed` or `cancelled`, or `wait_seconds` (0–3600) passes. Output:
  `{task_id, status, fields, truncated}`.
  - A task that ends `failed` or `cancelled` prints its result and fails the
    step with exit 5.
  - A deadline that passes fails the step with exit 3 and
    `deadline reached: last status <status>`.
  - Each request has its own 30 s limit, which covers its body: a server
    that trickles its answer is cut off. `WFX_CONVERSATION_REQUEST_TIMEOUT`
    in the env store sets 1–120 s.
- **Limits:**
  - at most 50 targets per call. Each is an E.164 phone (`+15550100`), an
    identifier (`A-Z a-z 0-9 . _ : -`, 1–128), or an object holding only
    `id` (an identifier), `phone` (E.164) and `name` (up to 128 printable
    characters);
  - 1–50 field names matching `^[a-z][a-z0-9_]{0,63}$`;
  - `task_type` and `channel` are 1–64 characters of `a-z 0-9 . _ -`;
  - the key is at least 16 characters;
  - the service's answer may be at most 1 MiB; a larger one, or a larger
    declared `Content-Length`, is refused;
  - a returned field value over 16 KiB is cut, ends in `…[truncated]`, and
    sets `truncated: true`.
- **Untrusted text:** targets and the collected fields come from outside, the
  fields from the people contacted. Treat them as untrusted text: don't put
  them into a prompt as instructions or into a script as code. Pass them on
  as data.
- **The key is never printed,** in any form a service might echo it back in
  (raw, JSON-escaped, base64, hex, URL-encoded).
- **Exit codes:**
  - 2: invalid input or configuration;
  - 3: deadline reached;
  - 4: the service refused, could not be reached, or answered too much or
    too slowly; its answer is shown without the key or the URL's query
    string;
  - 5: the task failed or was cancelled.

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
