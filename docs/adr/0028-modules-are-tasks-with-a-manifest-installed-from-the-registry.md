# 0028 — Modules are tasks with a manifest, installed from the registry

Status: proposed · 2026-11-08 · issues #82 #83 #84 (#85 follow-up)

## Decision

**A module is a task plus `module.yaml`** (`name`, `version`, `kind:
command|service`, `inputs`, `outputs`, `requires: {env, secrets}`, optional
`binary: {carry}`, and `service: {command, args}` for a service). Steps live in
the manifest or in `task.yaml` beside it. Unknown keys are a load error.

**Called like a GitHub Action, checked at load.** Inside `steps:` (flat or in a
job): `uses: name@major` + `with: {typed values}`. The call is replaced in place
by the task's steps (`<id>.<step>`); `needs: [<id>]` points at the call's last
steps. Unknown/missing/wrong-typed inputs and a wrong major fail the load.
Resolution order: local task → workflow (`workflow_call`) → installed module.
A `kind: service` module is never expanded into steps.

**Installed from the existing registry — no second one, no new UI.** A
`module.yaml` anywhere in an imported skill source (git import, pin to
tag/commit, Sync) is a module. `wfx module install <git-url> --ref v1` is
`wfx skills import --path .`; `wfx module sync` is the source's refresh;
`wfx module ls` (`GET /api/modules`) shows each module with what is still
**missing**; `wfx module config <m>` prompts once (echo off) for each and
stores it through the existing env store (secrets sealed). The source view
lists its modules.

**Service modules are single-instance processes that call hooks.** The first,
`modules/whatsapp-group-monitor` (`cmd/wfx-wa-monitor`): an exclusive lock per
account; ONE inbox read per tick for all watches via the existing
`apl-whatsapp.js receive` (cursor, echo ledger); dedupe by `chat|id`
persisted; per-chat 3 s debounce; POST `{message, messages[], watch, chat}`
to each watch's hook (ADR 0027) with a bearer token read from an env var the
watch names, and an `Idempotency-Key` so a retried batch never runs twice.
Watches are a JSON file re-read every tick.

## Consequences

- One mental model: everything reusable is a task; a module just adds a
  version, a type contract and its requirements.
- The in-engine subscriptions + router (ADR 0026, #76) duplicate what a
  service module + hooks now do. They are dead weight once the monitor is
  supervised; removal is a follow-up, not done here.
- Not yet: carry delivery of `binary:` (manifest field only), supervision +
  heartbeat on a worker, trust/signing and permission diff on Sync (#85).
