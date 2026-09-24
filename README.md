# wfnexus

**Typed pipelines for coding agents.** One Go binary and your repo. Nothing else, until you want it:
with no configuration at all it runs on SQLite and a folder, and `mode: server` moves it to your
Postgres and your bucket.

**Build the workflow inside the agent you already use. Publish it with its skills and MCP to a
server you host. Run it against any backend** — a CLI agent (`claude`, `codex`, `copilot`,
`opencode`), an ACP agent (`devin`), or your own HTTP model (OpenRouter, or ollama on localhost with
no key at all). Everyone else ships a runtime you must live inside: Mastra binds you to their
TypeScript runtime, gh-aw to GitHub plus Copilot, Devin and Jules are their own model. We ship **a
unit that travels**.

Nobody writes the YAML by hand either — an agent skill generates it inside your own session, and the
file is the **receipt**: readable, diffable, committable, reviewable by someone who never touched the
generator.

A workflow is a YAML file. A step is a **prompt + a scoped set of agent skills/tools + a JSON-schema
output contract**. The engine runs each step as a [toolnexus](https://github.com/muthuishere/toolnexus)
agent, refuses to advance until the step's output validates against its schema, persists every step's
state (SQLite or Postgres), streams the agent's tool calls to the UI over SSE, and stops for a human where the
workflow says so.

The first workflow is bug fixing: **validate → reproduce → draft PR → validate PR → publish PR.**

```
  validate-bug ──▶ reproduce-bug ──▶ draft-pr ──▶ validate-pr ──▶ [human approval] ──▶ finalize-pr
   skills:          skills:           skills:      skills:                             skills:
   validate-bug     reproduce-bug     fix-author   pr-reviewer                         pr-publisher
   repo-navigator   repo-navigator    repo-nav
   ↓ gate                ↓ gate                        ↓ gate
   valid=false →     reproduced=false →            approved=false →
   needs_input       needs_input                   fail (retry from draft-pr)
```

## Why this and not n8n / Devin / Copilot

Full analysis with sources: [`docs/research/the-pivot-2026-09-24.md`](docs/research/the-pivot-2026-09-24.md),
which supersedes what [`competitors-2026-09.md`](docs/research/competitors-2026-09.md) said about
**us** (its competitor facts stand).

| | this | Mastra | Archon | Windmill | n8n | GitHub agents | Devin/Jules |
|---|---|---|---|---|---|---|---|
| User-definable steps | YAML, generated | TypeScript | YAML | flows | nodes | agent profiles | no (fixed loop) |
| **One workflow, many execution backends** (CLI agent, ACP agent, or HTTP model) | **yes**, 23 providers | no (their TS runtime) | no | no | no | no (Copilot) | no (own model) |
| Schema-validated hand-off enforced *inside* the agent loop | yes, stored `jsonb` | schemas, validated around the step | undocumented | after the step | parser node | no (`outputs:` are strings) | no |
| **Static check with no model call** (`wfx dryrun`) | yes | yes (`mastra lint`) | no | no | no | no | no |
| Runs locally, off any forge, against a working tree | yes | no (Node project, `mastra build`) | yes | yes | yes | no | no |
| Self-host the executor | one Go binary (SQLite, or PG + S3) | yes, control plane is theirs | Bun | Rust + PG | Node (+Redis) | no | no |

Three honest corrections from the 2026-09-24 pass, because a comparison table that flatters us is
worse than none. All three cost us a row we had been selling:

- **Per-step scoped tools is gone as a claim, and the row with it.** GitHub's custom agents take
  `tools:` and `mcp-servers:` in a profile under `.github/agents/`
  ([docs](https://docs.github.com/en/copilot/reference/custom-agents-configuration)), and Mastra
  scopes per agent **and per call** — `activeTools` / `toolsets` / `clientTools` at `.generate()`
  time. Theirs is dynamic; ours is a static allowlist. We were behind, not ahead.
- **Typed step schemas are not ours as a category.** Mastra's `createStep()` takes input *and*
  output schemas (Standard JSON Schema — Zod, Valibot, ArkType), with TS type inference through
  `getWorkflow()` **and** runtime validation. What is still ours is narrower: the contract is
  enforced *in the loop* — `submit_output` is a tool whose validation failures come back as tool
  results, so the model corrects itself mid-turn rather than the run dying. And for typed edges
  specifically, `tsc` is a **stronger** check than `wfx dryrun`: it fails the build rather than a
  command you have to remember to run.
- **"Nobody ships a static validation pass" was false as written.** `mastra lint` exists —
  `--strict`, `--json`, `--preflight`, no model calls. The claim is true of the **canvas tier
  only**: six of six canvas builders test by really running. What `lint` and `tsc` cannot do is
  close a *generation* loop, because they check code a human already wrote; our dry run proves a
  workflow the generator has only just invented.

The surface that survived is **portability across execution backends** plus **generation with a
zero-token static proof**. If Mastra ships a provider abstraction that runs a workflow on a local
CLI agent, or gh-aw ships `needs:` with typed outputs between agentic jobs, the honest move is to
stop building a platform and ship the contract gate as a library — that kill criterion is written
down, in the pivot doc §7.

## Run it

Downloaded the binary? There is no step two:

```bash
./wfx-server      # 127.0.0.1:8090 — sqlite in ~/.local/share/wfnexus, artifacts in a folder
```

With no config file and no environment it takes the **local** defaults, migrates its own SQLite
file on boot, and serves the UI and the API out of the binary. Nothing to install, nothing to start.

**The default bind is `127.0.0.1:8090`, loopback only — it changed, and if you relied on remote
access you must say so.** There is no authentication yet (see [Status](#status)), so a default of
`:8090` would have put an unauthenticated server holding encrypted operator credentials on every
interface of the machine. Set `WFX_ADDR=:8090` to listen everywhere; the containers and the k8s
manifests state it themselves. Do not do it on a network you share until auth lands.

### Working on the repo

```bash
task infra:up     # Postgres :5460, MinIO :9030 (console :9031)
task api          # :8090 — WFX_MODE=server, so it meets that Postgres and bucket
task ui           # :5173 — Vite dev server, proxies /api
```

`task api` **states** `WFX_MODE=server`, because the no-config default is `local` and a working
session wants the real pair. `server`'s own defaults are exactly what `task infra:up` brings up, so
it needs nothing else; copy `.env.example` to `.env` to point it somewhere different.

Needs `OPENROUTER_API_KEY` (or `OPENAI_API_KEY` + `LLM_BASE_URL`) in the environment. Nothing reads
a secret into config — toolnexus picks the key up at call time.

### Storage: two modes, one shorthand

| | `mode: local` (the default when nothing says otherwise) | `mode: server` |
|---|---|---|
| state | SQLite at `~/.local/share/wfnexus/wfnexus.db` | Postgres (`DATABASE_URL`) |
| artifacts | a folder at `~/.local/share/wfnexus/artifacts` | S3/MinIO (`S3_ENDPOINT`, …) |

`mode` is a shorthand for a set of defaults, never a second code path. Anything you state yourself —
`storage.driver` in the file, `WFX_STORAGE_DRIVER` / `DATABASE_URL` / `WFX_ARTIFACT_DRIVER` in the
environment — beats it, in either direction. A value you state and get wrong **fails on boot**; it
never quietly falls back to SQLite. Precedence is environment > file > mode > built-in default.

The containers and the k8s manifests all set `WFX_MODE=server` explicitly, so a deployment gets
Postgres because it says so rather than because of where a default happens to sit.

## Install it

```sh
curl -fsSL https://raw.githubusercontent.com/muthuishere/wfnexus/main/install.sh | sh
```

Detects your OS and architecture, downloads `checksums.txt`, **refuses to install if the checksum
does not match or is not published**, and lands the binaries in `~/.local/bin`. `WFX_VERSION=v0.2.0`
pins a version instead of taking the latest. Windows: `install.ps1`, same behaviour.

macOS will quarantine a downloaded binary; the installer prints the release note's own workaround:

```sh
xattr -d com.apple.quarantine ~/.local/bin/wfx
```

`wfx version` says which binary you have, and where it came from:

```
wfx v0.1.0 (a71603f, 2026-09-24, go1.26.3)
```

### Offline, or air-gapped

Download once, verify it, carry it in. This is the path a site without egress actually uses, which
is why it comes before the registry:

```sh
# on a machine with a network
curl -LO https://github.com/muthuishere/wfnexus/releases/download/v0.1.0/wfx-server-linux-arm64.tar.gz
curl -LO https://github.com/muthuishere/wfnexus/releases/download/v0.1.0/checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing

# on the target, with no registry to pull from and no Go toolchain
tar xzf wfx-server-linux-arm64.tar.gz && cd wfx-server && docker compose up -d
```

There is one tarball **per Linux architecture**. It carries the linux binaries, the UI, workflows,
templates, skills, `registries.json`, the k8s manifests and its own Dockerfile, and that Dockerfile
*copies* the binaries rather than building them.

### From a registry, or from source

```sh
docker compose -f infra/docker-compose.yml up -d    # → http://localhost:8090
go install github.com/muthuishere/wfnexus/apps/api/cmd/wfx@latest
```

`go install` works for **`wfx`** and **`wfx-runner`**. It does **not** work for `wfx-server`: the UI,
workflows, templates, skills and provider registry are staged into `internal/assets/embedded/` by
`task assets:stage` and are not committed, so a `go install` of the server would build a binary that
serves a 404 at `/`. Use the installer or the tarball for the server.

Kubernetes manifests are in [`infra/`](infra/README.md). There is no chart and no operator: a
Deployment, a Service and a Secret.

### Versioning

`v0.x` while the API envelope and the workflow schema settle. Until `v1.0`: MINOR may break a
documented interface and says so in the release notes, PATCH never does. After `v1.0`, semver as
everyone else means it.

### Building a release yourself

```sh
task release     # 18 binaries, one tarball per linux arch, checksums.txt
```

Three binaries — `wfx-server`, `wfx`, `wfx-runner` — for linux, windows and macos on amd64 and
arm64. CI does exactly this on a tag and attaches the result to a GitHub Release; there is no
separate release tool to learn.

## The dry run, which costs nothing

`wfx dryrun <workflow>` answers "would this run **here**" without a model call, a clone, or a write.
It is the check the generator closes its own loop against, and it is the fastest thing in the repo.
Real output, this repository, today:

```
$ wfx dryrun code-review
code-review — sequential, 2 steps

wave 1           survey
wave 2           review

STEP             KIND    RUNS ON                      BUDGET
survey           prompt  anthropic/claude-sonnet-4.5  20 turns
review           prompt  anthropic/claude-sonnet-4.5  30 turns

ceiling: 50 model turns across 2 agent step(s)

would run.
```

The five-phase bug fixer, same command, same cost:

```
$ wfx dryrun bug-fix
bug-fix — sequential, 5 steps

wave 1           validate-bug
wave 2           reproduce-bug
wave 3           draft-pr
wave 4           validate-pr
wave 5           finalize-pr

STEP             KIND    RUNS ON                      BUDGET
validate-bug     prompt  anthropic/claude-sonnet-4.5  15 turns
reproduce-bug    prompt  anthropic/claude-sonnet-4.5  30 turns
draft-pr         prompt  anthropic/claude-sonnet-4.5  30 turns
validate-pr      prompt  anthropic/claude-sonnet-4.5  30 turns
finalize-pr      prompt  anthropic/claude-sonnet-4.5  15 turns

ceiling: 120 model turns across 5 agent step(s)

would run.
```

The waves are the DAG; `RUNS ON` is the model for an agent step, the interpreter for a `run:` step,
and the **label** for a placed one. When something is wrong it says so instead of printing `would
run.`, as `warning` or `FATAL` against the step and field: an env var the step reads and nobody set,
a `provider:` whose binary is not on **this** PATH, a `runs-on:` no online worker holds. Names only —
never a value — so a dry run is safe to paste into an issue.

## Backends

23 providers ship in `registries.json`. An adapter is a registry **entry**, not code
([ADR 0016](docs/adr/0016-an-adapter-is-a-registry-entry-not-code.md)), so adding one is a JSON
object, not a pull request. The same `prompt + skills + tools + output_schema` runs on all of them.

**Verified end to end on 2026-09-24** — each one answered a real prompt through the argv the registry
ships, in `TestLiveRegistryArgvs`, so the entry and its proof cannot drift:

| provider | kind | observed |
|---|---|---|
| `ollama-cli` | cli | 2.9s |
| `opencode` | cli | 3.8s |
| `claude-cli` | cli | 4.7s |
| `codex-cli` | cli | 12.7s |
| `copilot-cli` | cli | 18.3s |
| `devin` | acp | 14.8s |
| `ollama-http` | http | real 200, OpenAI-shaped body, from `localhost:11434` |

**Doc-verified, not run:** `anthropic`, `openai`, `openrouter` (+ its `gpt`/`haiku`/`sonnet`
shorthands), `groq`, `cerebras`, `deepseek`, `mistral`, `together`, `fireworks`, `xai`. Their base
URLs were read off each vendor's own documentation on 2026-09-24 and not one from memory; **the model
ids are unverified** and move fast. Two that a from-memory guess gets wrong: Groq needs the `/openai`
path segment, and Anthropic's is the bare host under `style: "anthropic"`.

**Self-hosted, and they need no key at all** — `ollama-http`, `vllm`, `lmstudio`, `llamacpp`. Until
today two enforcement sites demanded an API key for every `http` provider and reported
`NOT READY: provider llamacpp:  is not set`, naming no variable because there is none to name; both
are fixed and the four entries dropped `apiKeyEnv`. This is the bring-your-own-model argument: the
whole loop runs on a laptop, against a model on that laptop, reaching nothing. LM Studio and
llama.cpp document no model id — theirs is whichever model is loaded — so those entries carry the
placeholder `local-model`, which an operator **must** change.

`ollama-cli` has one real limit, named in its own description: the generic argv template appends the
model as a trailing flag and `ollama run` takes it positionally, so the entry bakes `qwen3:4b` in. A
step that wants to choose an ollama model uses `ollama-http`.

**`claude-cli` carries a legal caution, in the entry's description.** Anthropic's Consumer Terms,
Prohibited Uses, restrict accessing the Services "through automated or non-human means, whether
through a bot, script, or otherwise" **except via an Anthropic API Key**. A pipeline exec'ing
`claude` is script-driven access on a subscription seat; the clause's own carve-out is an API key,
which is what the `anthropic` http provider is. We enforce nothing and this is your call with your
own account — the entry says so rather than staying quiet. GitHub Copilot and Cognition/Devin have
no clause either way that we could find, and every OpenAI policy URL returned HTTP 403 to the
checking machine, so **no clause is asserted for `codex-cli`**. Details, with what was and was not
read, in [`docs/spikes/FINDINGS.md`](docs/spikes/FINDINGS.md) §08.

## What a run costs

Usage and cost are aggregated per step and written **while the step runs** — not at the end —
coalesced on a one-second deadline (`internal/engine/usage.go`). `select status, turns, usage from
step_runs` on a running step moves every second:

```
16:20:04|running|1|{"completionTokens":70,"costUsd":0.001357,"llmCalls":1,"promptTokens":1007,…}
16:20:12|running|6|… llmCalls:6 toolCalls:5 promptTokens:7192  costUsd:0.009017
16:20:16|done   |8|… llmCalls:8 toolCalls:7 promptTokens:10198 completionTokens:476 costUsd:0.012578
```

Two observed runs, both real, neither estimated:

- **`code-review` on this repo, sonnet-4.5 via OpenRouter: $4.37.** It completed — `survey` 4 turns,
  `review` 27 turns, both with validated typed output, 31 LLM calls, 30 tool calls. It also spent
  **1,424,154 prompt tokens against 6,541 completion tokens**, because there is **no context
  compaction**: the 117 KB diff was re-sent every turn. That ratio is the bill.
- **One step on local `qwen3:4b` via `ollama-http`: a true `$0.00`.** 2 turns, 740 prompt + 907
  completion, `"costUsd":0` — and the typed contract still held on a 4B model: `submit_output`
  validated and the step could not finish without it.

Free and unknown are different states. A priced provider reports `costUsd`; a local one reports a
real `costUsd: 0`; a provider with no price on its registry entry reports `costUnknown: true` and
**no `costUsd` key at all**, so no UI can render an unknown as `$0.00`. A step naming no `provider:`
runs on the process-wide default, which is not a registry entry and so has nowhere to carry a price —
any step that wants a cost must name a provider.

## Where a step runs

A job says `runs-on: windows`. That is a **label**, never a machine — the same thing it means in
GitHub Actions, and the same thing a Jenkins node label means.

A machine joins by running one command, which the **Workers** page (or `wfx workers`) hands you:

```bash
wfx-runner join --url https://wfx.example.com --token wfx_… --labels windows,devin
wfx-runner run
```

From then on that machine takes the steps whose label it holds, and runs them with the toolchain
installed **there** — the Devin CLI, a JDK, a signing certificate, a licence dongle. The platform
never connects to it: the worker polls out. So the platform can be a pod behind an ingress and the
machine can be a laptop behind NAT, and neither has to be reachable from the other.

**Agent steps travel too, and this is the interesting part.** A `prompt:` step placed on a worker
runs there as the *same agent*, built by the same code: its skills (carried as files — the worker
needs no skills directory), its scoped tools, its sub-agent team, its guardrails, its turn budget
and its `submit_output` schema gate. What does **not** travel is the CLI:

```yaml
- id: fix
  runs-on: windows
  provider: devin          # the devin on THAT machine's PATH, with ITS credential
  skills: [fix-author]     # carried there from here
  tools: [bash, read, write, edit]
  output_schema: { … }     # still enforced inside the loop, still validated
```

Its tool calls stream into the run log live, and its workspace diff comes back as the step's
artifact — the work happened on another disk, so the evidence has to be carried or the run would
record a step that changed nothing.

The result has exactly the shape a local step produces, so a gate reading `steps.build.ok` cannot
tell where it ran.

**Local is the default, and workers are additive.** The platform is its own client: a step with no
`runs-on:`, or one naming a label this process serves (`WFX_RUNNER_LABELS`, default `local`), runs
in the server process exactly as it did before workers existed. A single-machine install needs no
worker, sees an empty Workers page, and is not missing anything. You add a machine when a step
needs a toolchain this box does not have — not to make the platform work.

The default is `local` **alone** on purpose: `self-hosted` is the label a worker advertises, so if
the platform served it too, the first machine you joined would sit there online and idle while the
server quietly took its work. Joining with a label the platform already serves says so.

`wfx dryrun` says, for nothing, that a `runs-on:` nobody holds would wait:

```
STEP             KIND    RUNS ON                      BUDGET
build.whoami     run     buildbox (no machine)        30 turns
  warning build.whoami.runs-on   no worker online holds the label "buildbox" — this step would wait.
```

`wfx dryrun` tells the two failures apart before either costs anything: a `provider: devin` step
running *here* is fatal when `devin` is not on **this** PATH, and the same step placed on a worker
is checked against the pool instead — because whether this box has the CLI says nothing about the
machine that will run it.

The one thing that cannot be placed is a step using the platform's own authoring tools
(`workflow_catalog`, `workflow_validate`, `workflow_dryrun`) — those *are* this process, and the
step is refused at pack time rather than mid-run. [`infra/README.md`](infra/README.md) has the
rest, including running the worker as a service and what it does and does not isolate.

## Use it from your own agent

The authoring method is an agent skill, so you can run it inside whatever you
already use — Claude Code, or anything that reads `~/.claude/skills`:

```bash
wfx install --list                # what this platform ships
wfx install --skills              # copy them where your agents read skills
wfx install --skills workflow-author   # or just the one
```

It writes **both** global roots — `~/.claude/skills` and `~/.agents/skills` — because one machine
runs several agents and they do not share a root; `--to DIR` puts them somewhere else.

Explicit, like `playwright install`, and for the same reason: a tool that writes
into another tool's configuration behind your back is one nobody can audit. It
copies files, prints where they went, and undoing it is `rm -rf` on a directory
it names.

The skill interviews you, picks the cheapest node that answers each question
(`run:` before `judge:` before a full agent), drafts, dry runs its own draft
through `wfx`, fixes what that found, and only then hands over — with what it
assumed still attached.

## Templates

`wfx templates` (or the Templates page) lists starting points. The headline one is the five-phase
bug fixer: validate → reproduce → fix → review → publish, with the typed hand-off between phases
and the human gate before anything leaves the machine already wired.

```bash
wfx new bug-fixer --as my-bug-fix     # writes a workflow you own
wfx dryrun my-bug-fix                 # would it run here? costs nothing
```

**It ships with no skills on any phase, deliberately.** The five phases of a bug fix are the same
everywhere; what a good report looks like in your shop is not. A template gives you the shape and
the contracts — the expertise is the part you add.

A template is not a second kind of file and there is no template language: it is an ordinary
workflow carrying `template:`, loaded and validated by the same code as everything else, so a
template that would not run is caught at boot rather than by the first person who copies it. A
project ships its own the same way.

## Environment

Five layers, each overriding only the names it mentions:

```
system → project → workflow → job → step
```

The top two are the **platform's**, held in Postgres and **encrypted at rest** — system is what
every run on the machine gets (a proxy, a registry, a model key), project belongs to one repository,
because a token that can push to one repo has no business reaching a workflow from another.

```bash
wfx env                          # names and kinds; a secret's value is never shown
echo $TOKEN | wfx env set GH_TOKEN          # read without echo, never in argv or history
wfx env --project acme set DEPLOY_KEY
```

A secret goes in and never comes back out: the listing returns names, the API omits the value
field entirely, and the only path out of the database is into the process about to run a step. The
key lives in `WFX_SECRET_KEY`, or in a `0600` file written on first boot whose path is logged (never
its contents). Encryption protects a `pg_dump`, a backup on a laptop, a replica — not someone who
already runs the process, which is why `${VAR}` references remain better wherever they fit.

The bottom three are the **file's**, and a value may **name** a variable instead of holding one:

```yaml
env:
  SERVICE_URL: https://api.example.com   # a literal: it is committed, so it is not a secret
  API_TOKEN: ${GITHUB_PAT}               # a reference: read where the step runs
```

The distinction is the whole design. A reference is only a name, so the value is never written to
the file, never stored in the run, never logged, and never sent to a worker — it is read from the
environment of whatever machine executes the step. A build box's own credential is therefore usable
there without the platform ever holding it. A credential written out in full is **refused on save**,
because the file is committed and saving it is the leak.

All of it reaches `run:` steps, the agent's `bash` tool, and an agent CLI's own process. `wfx dryrun`
lists the variable names a step reads and fails if one is unset — names only, so a dry run is safe
to paste into an issue.

## How a step works

```yaml
- id: reproduce-bug
  skills: [reproduce-bug, repo-navigator]     # only these SKILL.md files are loaded
  tools:  [bash, read, write, edit, grep, glob, todowrite]   # only these builtins exist
  mcp:    []                                  # per-step allowlist from mcp.json
  max_turns: 40
  prompt: |
    Validated bug summary: {{ .Steps.validate-bug.summary }}
    …
  output_schema:                              # becomes the `submit_output` tool's input schema
    type: object
    required: [reproduced, method, steps, evidence, root_cause_hypothesis]
    properties:
      reproduced: { type: boolean }
      method:     { type: string, enum: [failing_test, script, manual, not_reproduced] }
      …
  gates:
    - field: reproduced
      equals: false
      action: needs_input     # pause the run and ask the human
      message: "Could not reproduce. {{ .Output.root_cause_hypothesis }}"
```

The contract is enforced *inside* the agent loop, not after it:

1. The step's `output_schema` becomes the input schema of a native `submit_output` tool.
2. When the model calls it, the value is validated. Failures come back **as the tool result**, so the
   model self-corrects in the same loop instead of the run dying.
3. A toolnexus `Completion` gate refuses to let the step finish until a submission was accepted —
   up to `max_attempts`, then the step stops loudly.

Prompts are Go templates over `.Input`, `.Steps.<step-id>.<field>`, `.WorkDir` and `.RunID`
(hyphenated step ids work — they are rewritten to `index` lookups).

Gates: `needs_input` (pause, ask), `fail` (stop), `skip_to` (jump).

## Isolation

Each run gets its own **git worktree** of the target repo (`isolate: true`, the default), so N runs
can work the same repository in parallel without fighting over the index or HEAD. `repo_url` clones
per run instead. Set `isolate: false` to let the agent work directly in your checkout.

## Layout

```
apps/api/            Go: engine, workflow loader, SQLite/Postgres store, folder/S3 blobs, REST+SSE
  internal/engine/   the run loop, per-step agent, schema validation, event broker
  internal/workflow/ YAML loader + prompt templating
  migrations/        golang-migrate SQL (embedded, applied on boot)
  cmd/wfx-runner/    the worker: joins a pool by label, takes steps, reports
apps/ui/             React + TS + Vite: workflow list, run form, live run view
infra/               Dockerfile, compose, k8s manifests — the whole deploy story
workflows/*.yaml     the workflows
skills/*/SKILL.md    the agent skills each step may load
mcp.json             MCP servers steps may be granted
```

Placement: `workers` (a machine and its labels) → `worker_jobs` (one step's work, queued for
whoever holds the label). `FOR UPDATE SKIP LOCKED` is what makes two machines on the same label
safe without a lease table.

State: `workflow_runs` → `step_runs` (status, attempts, turns, validated `output jsonb`, usage) →
`run_events` (the full activity log, replayed into the UI). Artifacts (agent transcript,
`git diff` of the workspace per step) go to S3 and are linked from the run.

## API

```
GET  /api/workflows                    POST /api/workflows/reload
POST /api/workflows/{name}/runs        GET  /api/runs            GET /api/runs/{id}
GET  /api/runs/{id}/events             SSE, ?after=<id> replays the backlog
POST /api/runs/{id}/approve|reject|input|retry|cancel
GET  /api/workers                      the pool + the join command
POST /api/workers/join|claim|heartbeat|jobs/{id}/result   the whole worker protocol
GET  /api/runs/{id}/artifacts/{id}     302 → presigned S3 (or ?inline=1)
```

## Status

Working end to end against a real repo with a real bug, on seven backends, with typed output and a
per-step cost. What is **not** built, plainly, because the repo's own rule is that a document which
flatters is worse than none:

- **No authentication and no authorization.** Anyone who can reach the port is an admin. The
  loopback default above mitigates it and does not fix it; the actor on an approval
  (`X-WFX-Actor`) is a *claim*, not an identity — the shape is right and nothing checks it.
  [ADR 0017](docs/adr/0017-identity-is-the-device-grant.md) picks the device grant (RFC 8628) and is
  the next thing after distribution.
- **No release has been cut yet.** The install path above is built and tested — CI, a checksum-
  verifying installer, per-arch tarballs — but there is no git tag, so there is nothing at those
  URLs until the first one. The workflows have also never executed: their YAML and the scripts they
  call are verified, a real Actions run is not.
- **No publish direction.** Everything the server knows it read off a disk at boot. There is no
  `wfx publish` and no registry to publish to — [ADR 0018](docs/adr/0018-the-registry-has-a-publish-direction.md)
  is the decision, not the code.
- **No evals.** Nothing here can show that a workflow passing on sonnet still passes on `qwen3:4b`.
  That makes "runs anywhere" an unproven claim, which is why
  [ADR 0019](docs/adr/0019-evals-are-the-proof-of-portability.md) promotes evals from a skipped gap
  to the proof of the core claim.
- **No context compaction.** The $4.37 above is what that costs. toolnexus ships `agents.Compactor`
  and we do not use it; we cap turns instead, which stops work rather than continuing it.
- **No Prometheus or OTEL export.** Usage and cost are aggregated live per step and stored on
  `step_runs.usage` — that part is done — but there is no metrics endpoint and no trace export, and
  nothing rolls the per-step figures up to the run.
- Also open: a queue/limit for concurrent runs, and the "safe outputs" split that keeps a GitHub
  write token out of the LLM steps.
