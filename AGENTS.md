# Agent instructions — wfnexus

Go + React platform that runs **declarative agent workflows** on the `toolnexus` SDK.
Read `README.md` first; it is short and accurate.

## Invariants — don't break these

1. **A step's contract is enforced inside the loop.** `output_schema` → the `submit_output` native
   tool → validation errors return as the tool result so the model self-corrects. Never "parse the
   final text as JSON"; that is the failure mode this design exists to avoid.
2. **Scoping is the security model.** A step gets exactly the skills / builtins / MCP servers its
   YAML lists. Never widen a step's toolkit to make something work — change the YAML.
3. **Never silently succeed.** A step that stops without an accepted `submit_output` is an error
   with the toolnexus `Status`/`StoppedBy` in the message. Run status is one of
   `queued|running|awaiting_approval|needs_input|done|failed|cancelled` and always reflects reality.
4. **Secrets stay out of config.** toolnexus reads `OPENROUTER_API_KEY`/`OPENAI_API_KEY` at call
   time. Do not add API keys to `config.Config`, log them, or write them to a workflow file.
5. **Outward-facing steps are approval-gated.** Anything that pushes, opens a PR, or posts
   externally sets `requires_approval: true`. Don't remove a gate to make a demo smoother.
6. **Migrations are append-only.** New `NNNNNN_name.up.sql` + `.down.sql` in `apps/api/migrations/`.
   Never edit an applied migration.

## Where things live

- `internal/engine/engine.go` — the run loop: step ordering, gates, approvals, resume, workspace.
- `internal/engine/step.go` — one step = one toolnexus agent (toolkit assembly, hooks, artifacts).
- `internal/engine/schema.go` — JSON-schema compile + model-readable validation errors.
- `internal/workflow/` — YAML loader and prompt templating (hyphenated step ids are rewritten).
- `internal/assets/` — the UI bundle, templates, skills and registries.json compiled into
  `wfx-server` by go:embed, so a downloaded binary works alone. Disk ALWAYS wins; embedded is the
  fallback. Staged by `task assets:stage`, which every server build depends on.
- `internal/store/` — pgx queries. `internal/blob/` — MinIO/S3. `internal/api/` — chi routes + SSE.

## Working on it

```bash
task infra:up && task api        # :8090   task ui   # :5173
cd apps/api && go test ./... && go vet ./...
```

Adding a workflow: drop a YAML in `workflows/`, then `POST /api/workflows/reload` (or the UI's
"Reload YAML" button) — no restart. Adding a skill: `skills/<name>/SKILL.md` with `name` +
`description` frontmatter, then list it under a step's `skills:`.

<!-- ctx-optimize:begin -->
<ctx-optimize>
  <precondition>Run `command -v ctx-optimize` first. If it is NOT installed, IGNORE this entire
  block and answer by reading the code normally — the store is an optimization, not a requirement
  (install later with `npm install -g @muthuishere/ctx-optimize`, or download the binary). Everything
  below applies ONLY when the command exists.</precondition>
  <store>MULTI-MODULE repo, pre-built knowledge store at `~/ctxoptimize/wfnexus/` — one graph per module + a navigator, 4 modules declared in `.ctxoptimize/config.json`.</store>
  <use>Use it INSTEAD of grep-and-read chains — PICK BY INTENT: find → `ctx-optimize query "<terms>"` ·
  inspect a symbol → `card <symbol>` · about to EDIT → `change-plan <symbol>` (callers+impact+tests, one
  call) · blast radius → `affected <symbol>` · connection → `path <a> <b>` ·
  list/filter (no jq): `nodes --kind K` / `edges --relation R` / `deps --scope dev`.
  Scope follows your cwd: a module dir answers from that module (zero hits escalate repo-wide); the root
  federates via the navigator (`~/ctxoptimize/wfnexus/navigator.md`; `--modules all|a,b` widens).
  Output is parsed fact with exact file:line — cite it directly, do NOT re-verify in source.
  Exhaustive literal-string sweeps stay grep's job.</use>
  <deep-doc>The FULL usage card — verify discipline, store-vs-grep ladder, sources (databases/
  buckets/queues/APIs by env-var name), remote push/pull, `up` — is committed at
  `.ctxoptimize/instructions.md`. Read it before deeper store work.</deep-doc>
  <no-local-store>Fresh clone with nothing at `~/ctxoptimize/wfnexus/`? Run `ctx-optimize up` —
  it pulls the team's prebuilt store when the config declares one, otherwise rebuilds every module store in seconds.</no-local-store>
</ctx-optimize>
<!-- ctx-optimize:end -->
