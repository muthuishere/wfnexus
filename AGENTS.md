# Agent instructions — bug-fixer-platform

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
- `internal/store/` — pgx queries. `internal/blob/` — MinIO/S3. `internal/api/` — chi routes + SSE.

## Working on it

```bash
task infra:up && task api        # :8090   task ui   # :5173
cd apps/api && go test ./... && go vet ./...
```

Adding a workflow: drop a YAML in `workflows/`, then `POST /api/workflows/reload` (or the UI's
"Reload YAML" button) — no restart. Adding a skill: `skills/<name>/SKILL.md` with `name` +
`description` frontmatter, then list it under a step's `skills:`.
