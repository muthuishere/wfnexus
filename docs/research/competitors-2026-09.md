# Competitors — decision pass, 2026-09-23

*Supersedes nothing. [`competitive-landscape.md`](competitive-landscape.md) (2026-09-21) stays: it
surveys coding-agent products and is still accurate. This file covers what that one under-weighted —
durable-execution frameworks, canvas builders, and Actions-shaped repo tooling — and turns it into
decisions.*

**Sourcing rule used here:** *shipped* = in reference docs or a tagged release. *preview* = labelled
so by the vendor. *unlabelled* = the vendor states no maturity; treated as preview. *unverified* = I
could not confirm from a primary source and say so.

---

## 1. What changed since the 21st

| fact | evidence | so what |
|---|---|---|
| **GitHub ships per-repo agent profiles**: `.github/agents/<name>.md`, YAML frontmatter `name` / `description` / `prompt` / `tools:` / `mcp-servers:` (with `${{ secrets.* }}`), also org- and enterprise-level | [docs](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-custom-agents), [config ref](https://docs.github.com/en/copilot/reference/custom-agents-configuration) | **"Scoped skills/tools per agent, in the repo" is now table stakes.** Our README implies it is a wedge. It is not. What GitHub still does *not* document is chaining those agents or typing what passes between them. |
| **Charlie Labs is shutting down** — "Charlie will remain available until October 5, 2026" | [notice](https://charlielabs.ai/blog/charlie-is-shutting-down/) | Drop from the landscape. The markdown-daemons-in-repo shape did not survive commercially. |
| **gh-aw grew a lot**: `safe-outputs:` now has 40+ handlers (`create-pull-request`, `push-to-pull-request-branch`, `dispatch-workflow`, `assign-to-agent`, `jira-create-issue`…), plus `network:`, `max-turns:`, `max-ai-credits:`, `skills:`, `plugins:`, `strict:`. Chaining only via `dispatch-workflow` (max 3) / `call-workflow` (max 1). Still **one agent per workflow**; no `needs:` between agentic jobs, no typed handoff | [frontmatter](https://github.github.com/gh-aw/reference/frontmatter/), [safe-outputs](https://github.github.com/gh-aw/reference/safe-outputs/) | Closest competitor by *shape*. Our wedge survives contact, narrowly: the typed multi-agent DAG. |
| **Earthly is sunset** (announced 2025-04-16, hosted services off 2025-07-16, "no longer accepting PRs"); Cosine retired the Genie branding for on-prem models (third-party sources, **unverified**) | Earthly blog/repo | Neither is a competitor. Earthly's lesson is in §3. |
| **Actions YAML + expressions already exist as MIT Go libraries** | below | We may be writing a parser someone ships. |

---

## 2. The libraries we should not reimplement

| library | licence | what it gives | status |
|---|---|---|---|
| **`nektos/act/pkg/exprparser`** | MIT, v0.2.89 | A full **evaluator** of the Actions expression language, importable (under `pkg/`, not `internal/`): `NewInterpeter(env *EvaluationEnvironment, config)`, `Evaluate`, `IsTruthy`, `DefaultStatusCheck{None,Success,Always,Canceled,Failure}` — the implicit `success()` on a bare `if:` is already modelled. `EvaluationEnvironment` has typed `Github, Env, Job, Steps, Runner, Secrets, Vars, Strategy, Matrix, Needs, Inputs`. | shipped |
| **`rhysd/actionlint`** | MIT, v1.7.12 | Workflow **parser + AST** (`Parse()` → `Workflow/Job/Step`) and an expression **type-checker** (`ExprSemanticsChecker`, `ObjectType`…). Type-checks, does not evaluate. [api.md](https://github.com/rhysd/actionlint/blob/main/docs/api.md) | shipped |
| **`gitea.dev/actionslib`** | MIT, v1.1.0, published 2026-09-18 | Gitea's library-first extraction: `pkg/model`, `pkg/exprparser`, `pkg/expreval`. Avoids act's Docker dependency. | shipped, very new |

act itself parses with actionlint's AST and evaluates on top — that is exactly the division of labour
we want: **actionlint in `dry-run`, exprparser at runtime.** There is no GitHub-published JSON Schema;
the de-facto one is SchemaStore `github-workflow.json`, which validates `if:` only as
`^\$\{\{(.|[\r\n])*\}\}$` — no semantics. GitHub's reference implementation
(`actions/languageservices`, MIT, TypeScript) is useful as a spec oracle only.

Also worth knowing: **`ChristopherHX/runner.server`** (MIT) clones a repo and schedules a whole
Actions workflow locally with GitHub-faithful expression evaluation — the closest prior art to
`wfx import`. `actions/runner` itself is **not** prior art: it never reads workflow YAML; GitHub's
server parses and schedules. Depot / Blacksmith / Namespace / WarpBuild are label-swap runner
vendors (`runs-on: depot-ubuntu-24.04`) — GitHub still schedules, so they are orthogonal, not
competitors. Nx Cloud reads no Actions YAML at all (`.nx/ci-config.yaml` over the Nx task graph).

---

## 3. Durable execution — the question we actually had

We listed "snapshot resume" in `not-now.md`. The research says: **the question is malformed.** There
are two camps, and the one we'd have built is the wrong one.

- **Snapshot camp** — LangGraph checkpointers, MS Agent Framework `WithCheckpointing`: serialise
  whole graph state per superstep. This is the expensive thing we skipped. Keep skipping it.
- **Memoised-step camp** — DBOS, Inngest, Temporal, Flyte: persist each **step's output**; on
  recovery, re-drive the graph and skip any step that already has one. DBOS's documented schema is
  literally `workflow_status` + **`operation_outputs`** (one row per step: output, error, timing);
  Go SDK `dbos-transact-golang` v1.4.0, MIT.

**We already have `step_runs.output jsonb`.** Resume-by-memoisation is a `WHERE output IS NOT NULL`
skip, not a subsystem. Temporal's full event-sourced replay is the costly variant — it needs
determinism discipline (`workflow.Now`, `SideEffect`, `GetVersion`) and hits a hard 51,200-event /
50 MB history cap forcing Continue-As-New. Our control flow is declarative; there is no program to
replay. **Do not copy Temporal here.** Also: DBOS keys steps by *positional* `function_id`, which
breaks on reorder — key on our YAML step id instead.

Earthly's lesson, since it is the only corpse in the set: it made the OSS executor depend on hosted
Satellites, then turned them off. Monetise a control plane, never the executor.

---

## 4. Things I verified that contradict our own claims

- **LangGraph's `response_format` does not enforce mid-loop.** The structured response lands in
  `structured_response` on the agent's **final** state; `ToolStrategy` retries validation,
  `ProviderStrategy` uses native structured output — both terminal. CrewAI's `output_pydantic` /
  guardrails validate *after* the task. **In-loop enforcement via a `submit_output` tool whose
  failures return as tool results is genuinely ours.** That claim holds.
- **Per-step scoped tools is not ours** (see §1, GitHub custom agents).
- **Actions' own `outputs:` are untyped strings.** `workflow_call` inputs are typed only
  `boolean|number|string`; workflow outputs are `description` + `value` mapped from a job output
  ([docs](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows)). So a
  JSON-schema contract is *beyond* Actions vocabulary and we cannot borrow a word for it — but we
  can attach it to the word `outputs:` rather than invent a new key.
- **Nobody in the canvas tier ships a real static validation pass.** Six for six, every "test" is a
  real run costing real tokens. The near-exception is Dify's pre-publish **Checklist** (missing node
  config, no model call) — and its doc page 404s. **Our dry run is the clearest unoccupied ground in
  this survey.** We are under-selling it.
- **Branching is a node, never an edge property** — six for six across n8n, Windmill, Activepieces,
  Dify, Flowise, Langflow. Confirms `if:` on a job is the right shape. No invention needed.

---

## 5. Product notes — only the decision-changing line

| product | what / licence | the ONE thing to want | the ONE thing NOT to copy |
|---|---|---|---|
| **gh-aw** | Markdown+frontmatter compiled to a real `.lock.yml`; unlabelled preview, v0.88.8 | **Safe outputs**: agent job runs read-only and *emits* structured effect requests; separate permission-holding jobs validate and apply, with per-handler `max:` caps. Prompt injection becomes a permissions problem. | The 40+ first-party handler catalogue — every capability needs a compiler change. Ship **one** generic validated-effect interface with the same propose/apply split. |
| **Dagger** | Apache-2.0; v0.21.9 stable, **1.0 still beta**; `LLM` core type (`dag.LLM().WithEnv().WithPrompt()`), GA wording **unverified** | The `Env` object as a typed contract (`withStringInput`, `withContainerOutput`) — typed slots buy content-addressed caching of step results | Modules-as-code: every step author writes and builds Go/TS. That erases our YAML surface. |
| **Flyte / Union** | Apache-2.0, K8s | **`flyte rerun --recover`** (reuse succeeded actions, re-run only failures) and `--action-name` to re-run one action with its exact recorded inputs. Also: typed edges validated at **compile** time. | The K8s propeller + register/image-build loop. Note `--recover` does not work in their local mode — that is our edge. |
| **Restate** | runtime BSL 1.1 → Apache-2.0 after 4y (prod self-host explicitly allowed), SDKs MIT, real Go SDK | **Awakeables**: `restate.Awakeable[T](ctx)` → `.Id()` / `.Result()`, resolved externally by `ResolveAwakeable(ctx, id, value)`. One table + NOTIFY buys an approval gate. | Services-as-HTTP-endpoints behind a second daemon. We are one Go binary on one box. |
| **Trigger.dev** | Apache-2.0, documented self-host | `schemaTask` validates **before the run row exists**, and `TaskPayloadParsedError` **skips retries** — "schema failures are not retryable" is a rule we should have | CRIU checkpoint-restore is Cloud-only; wrong shape for a Go binary. |
| **Prefect** | Apache-2.0 | Transactions: `@task.on_rollback` — rollback hooks undo an agent's side effects (delete the branch/PR it opened) | Result persistence is **off by default**, so caching silently no-ops. Persist every step output unconditionally. |
| **Dagster** | Apache-2.0 core; Dagster+ $100/mo + $0.035/credit | **Dagster Pipes** (`PipesSubprocessClient`, `PipesContext.report_asset_materialization(metadata=…)`): a wire protocol for an agent **subprocess** to report typed output and logs back **without a container sandbox** — exactly ADR 0015's world. Also 1.13 shipped a YAML DSL (`defs.yaml`) — precedent that YAML-first is not a downgrade | The asset/materialisation model: drags in partitions, backfills, freshness for no gain. |
| **LangGraph** | lib MIT; Platform Plus $39/seat/mo, **self-host Enterprise-only** | `get_state_history()` time travel | The reducer-merged shared `State` dict — it dissolves "a step emits one typed object". |
| **n8n** | Sustainable Use License — **multi-tenant hosting forbidden**; git sync is Business+ | **"Copy to editor" / "Debug in editor"**: take a past execution and pin its data onto a node, so a production failure is re-debugged with no re-fetch | Pin data being **dev-only** ("not available for production workflow executions"). |
| **Windmill** | AGPLv3 + proprietary `enterprise` flag; CE git sync capped at 2-user workspaces | **Restart from step**: creates a *new* run that **copies predecessor step statuses** rather than re-running them. Right for steps that cost money | It is a premium feature, and its branch/loop granularity leaks flow internals. |
| **Dify** | Apache-2.0 **plus** no-multi-tenant-without-authorisation and no-logo-removal clauses | **Variable Inspector**: upstream node outputs cached *and editable*, so you re-run one node against mocked upstream values | DSL export as the git story — it strips API keys and KB content, so the round trip is lossy. |
| **Activepieces** | MIT except `packages/ee/` — and **git sync lives in `ee/`** | A step's data is **not selectable downstream until that step has been tested** — a cheap integrity rule for typed handoff | It substitutes that guardrail *for* validation; there is no static pass at all. We have the schema; enforce statically. |
| **Langflow** | MIT | **Freeze**: pin a component's last output and freeze everything upstream, explicitly to avoid repeat API calls | Their frozen-vertex cache was not scoped per-principal (PR #15191) — a cache-leak bug. Scope any output cache by run + identity from day one. |
| **Flowise** | Apache-2.0, `/enterprise` carved out; **no anti-hosting clause** | Agentflow V2 **Human Input** node with checkpoints that survive restart | `flowData` stored as a stringified blob — one-line git diffs. |
| **CrewAI / AutoGen** | MIT / maintenance mode | CrewAI **Flows** (`@start`/`@listen`/`@router`) — the declarative half | **AutoGen has been in maintenance mode since Oct 2025**; do not benchmark against it. Ignore CrewAI Crews (role-play delegation, nondeterministic by design). |

**Run-inspection views worth copying:** Temporal's (History as Timeline / All / Compact / **raw JSON,
downloadable**, plus Input-and-Results payloads, Pending Activities, Call Stack, Relationships) and
Inngest's two-panel timeline-plus-detail where **each retry is its own span with an "Attempt 2"
badge**. The latter is ideal for nondeterministic agent steps.

---

## ADOPT — ranked

1. **Import the expression evaluator instead of writing one.** `rhysd/actionlint` (`Parse` +
   `ExprSemanticsChecker`) inside `wfx dry-run`; `nektos/act/pkg/exprparser` (or
   `gitea.dev/actionslib/pkg/expreval`, lighter — no Docker dep) for runtime `if:`. Both MIT.
   *Cost:* a day to swap, one dependency, and we inherit GitHub-faithful `success()` / `always()`
   semantics we would otherwise get subtly wrong. *Vocabulary:* none invented — this is the opposite.
2. **Resume by memoised step output, and close the snapshot question.** Re-drive the DAG on retry;
   skip any step whose `step_runs.output` is non-null and whose inputs hash unchanged.
   *Ours:* `wfx run --recover` (Flyte's verb) and `wfx run --only <step-id>` re-running one step
   against its recorded inputs. *Cost:* ~a day, plus an input-hash column. *Moves an entry out of
   `not-now.md` by making it cheap rather than by meeting its trigger.*
3. **Safe outputs: split propose from apply.** A `prompt` step never holds a write token; it emits a
   typed effect object, and a `run` step with the credentials applies it. *Ours:* one generic
   `effects:` block on a job with a `max:` cap — **not** gh-aw's 40-handler catalogue.
   *Cost:* moderate; it is the biggest single security win available pre-sandbox, and it is already
   on the README's "not yet done" list.
4. **Pinned step outputs as first-class fixtures.** Store a past run's validated step outputs and let
   `wfx dry-run` / a re-run consume them, so authoring a workflow costs no tokens.
   *Ours:* `wfx pin <run-id> <step-id>` writing a fixture next to the workflow in `.wfx/`.
   *Cost:* small, and it is the multiplier on the dry run we already have. n8n keeps this dev-only;
   with schema-validated handoff we do not have to.
5. **Schema failures are not retryable** (Trigger.dev's `TaskPayloadParsedError`). Distinguish "the
   model produced an invalid object" (retry in-loop, already done) from "the *workflow* declares a
   contract the producer cannot satisfy" (fail loudly, do not burn `attempts`).
   *Cost:* an error-kind constant. This is the cheap half of the `not-now.md` "failure kinds" row.
6. **Run-inspection UI: one attempt = one span.** Copy Inngest's badge, and Temporal's downloadable
   raw-JSON history. *Cost:* UI only. High value because agent steps are nondeterministic and the
   question is always "how did attempt 2 differ".
7. **Rollback hooks** (Prefect `on_rollback`): a failed run should be able to delete the branch and
   close the draft PR it opened. *Cost:* small; maps to a `run` step gated on `if: failure()` — a
   word Actions already has.

## REJECT — ranked

1. **Temporal-style deterministic replay.** Buys nothing over memoised steps and imports a
   determinism discipline plus a 51,200-event history cap. *Changes our mind:* never, unless the
   engine itself becomes user-programmable code rather than YAML.
2. **A visual canvas as the source of truth.** All six builders make the DB row authoritative and the
   file an export; Dify's is lossy, Flowise's is a stringified blob. Our `.wfx/workflows/*.yml`-as-
   truth is a real differentiator. *Changes our mind:* nothing — a **read-only** canvas rendered from
   the YAML is fine and is a different thing.
3. **Edge-level conditions / a bespoke branch node.** Six for six put branching in a node, and we
   already have `if:`. Building `condition:` on an edge would break the never-invent rule for zero
   gain. *Changes our mind:* nothing.
4. **A handler catalogue for effects** (gh-aw's 40+). Every new capability becomes a compiler change.
   *Changes our mind:* if one generic effect interface proves unsafe in practice.
5. **Modules-as-code as the authoring surface** (Dagger, Dagster's Python). Our whole claim is that a
   workflow is a YAML file a reader already understands. *Changes our mind:* if `run` steps prove
   insufficient for real work — and even then the answer is `uses:`, not a Go SDK.
6. **A shared mutable graph state object** (LangGraph's reducers). Directly contradicts ADR 0003.
7. **Hosted-only durability tricks** (Trigger.dev CRIU, LangSmith). Wrong shape for one Go binary.
8. **Monetising by making the executor depend on a hosted service.** Earthly did exactly this and is
   dead. *Changes our mind:* nothing.

## Where we are actually differentiated — sceptical version

**Genuinely ours, on this evidence:**
- **Schema enforcement *inside* the agent loop.** LangGraph, CrewAI and MS-AF all validate after the
  node returns. Failures-as-tool-results so the model self-corrects in the same loop has no
  counterpart I found.
- **Typed object handoff between two *agent* steps.** gh-aw is one agent per workflow; GitHub custom
  agents document no chaining; Actions' own outputs are untyped strings. Still open.
- **Dry run with no model call.** Nobody in the canvas tier ships one. We under-sell this badly — it
  is arguably the most defensible thing we have and it is a footnote in the README.
- **Actions syntax run *outside* GitHub over a repo's own `.wfx/`.** Only `runner.server` is close,
  and it is a faithful-CI project, not an agent platform.

**Table stakes we should stop claiming:**
- **Per-step scoped skills/tools/MCP.** `.github/agents/*.md` has `tools:` and `mcp-servers:`. The
  README's comparison table needs this row corrected.
- **Self-host, YAML workflows, approval gates, per-step re-run.** All present across Windmill,
  Archon, Activepieces, Flowise.

**Where we are behind:** no output caching or input-hash memoisation (Dagger, Langflow, Windmill all
have a form of it); no per-step input pinning (Dify's Variable Inspector is better than anything we
have); no attempt-diffing run view (Inngest); no propose/apply token split (gh-aw shipped it); and
the README is now stale on both `.wfx/` and the Actions-syntax framing.

## Biggest risk

**GitHub.** Not Devin, not Archon. GitHub already owns the syntax we adopted, the repo the config
lives in, the runner, and now per-repo agent profiles with scoped tools. gh-aw is the same shape as
us, one layer down, compiling to real Actions.

For this project to become pointless, three things must all become true:
1. gh-aw gains `needs:` **between agentic jobs** with structured outputs passed between them —
   today it explicitly does not; chaining is `dispatch-workflow` (max 3) fan-out.
2. Those outputs become **schema-validated contracts** rather than free text.
3. Running the same workflow **locally, off GitHub, against a working tree** stays as bad as it is
   (`act` does not support `job.permissions`, `concurrency`, `job.timeout-minutes`,
   `continue-on-error`, OIDC, or a complete `github` context).

Items 1 and 2 are squarely in GitHub Next's path and could land in a single release. **Item 3 is our
moat, and it is the one we control.** The strategic read: lean into *off-GitHub, local, against your
checkout, no cloud round trip, no tokens to dry-run* — and treat typed multi-agent chaining as a
lead we are racing to monetise, not a durable one.

---

### Not verified
Dagger `LLM` GA wording and 1.0 date; gh-aw's maturity label (none stated); CrewAI `@persist`
primary doc; MS Agent Framework 1.0 Go API (sourced, not self-verified — post knowledge cutoff);
Cosine's 2026 status (third-party only); Depot Registry; Namespace / WarpBuild specifics; n8n's
on-disk git layout; Dify Checklist's exact rules (doc 404s); Flowise Agentflow V2 traces from
primary docs.
