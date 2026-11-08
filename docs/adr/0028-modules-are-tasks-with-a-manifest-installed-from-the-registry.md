# 0028 — Modules are tasks with a manifest, installed from the registry

Status: proposed · 2026-11-08 · issues #82 #83 #84 (#85 follow-up)

## Decision

**A module is a task plus `module.yaml`** (`name`, `version`, `requires: {env,
secrets}`, optional `binary: {carry}`, and any of: its own `inputs`/`steps`
(or `task.yaml`), named `commands: {name: {inputs, outputs, steps}}`, and
named `services: {name: {command, args, inputs}}` — #109). A command is called
`<module>.<command>`, a service is `<module>.<service>`. Unknown keys are a load
error.

**Called like a GitHub Action, checked at load.** Inside `steps:` (flat or in a
job): `uses: name@major` + `with: {typed values}`. The call is replaced in place
by the task's steps (`<id>.<step>`); `needs: [<id>]` points at the call's last
steps. Unknown/missing/wrong-typed inputs and a wrong major fail the load.
Resolution order: local task → workflow (`workflow_call`) → installed module.
A service is never expanded into steps. `wfx module call <module>.<command>`
and `POST /api/modules/{name}/call` run a command on its own: a generated
wrapper workflow of the same name (never what `uses:` resolves to), so a call is
an ordinary, attributed, approval-gated run.

**Preinstalled `apl`, profiles, the outbox (#109).** `modules/apl` ships with
the server (`WFX_MODULES_DIR`): service `whatsapp` (`wfx-wa-monitor`) and
commands `whatsapp-send`, `mail-send`/`mail-draft`/`mail-list`, `teams-send`,
`calendar-list`/`calendar-create`, `drive-get`/`drive-put`, `contacts-find`,
`find`. A module has named **profiles** (a handle — the account — plus input
defaults), one the default, set once globally (`wfx module profile set apl
business --account biz --default`); `profile:` picks another. A module's
command steps and services get `WFX_MODULE_TOKEN`; with it
`/api/module-self/{module}/outbox` queues a send on the running service of the
profile's account, which drains it through its adapter — one writer per
number, a key sent once. Sends default to `draft`; `approval` waits for a
person.

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
persisted; per-chat 3 s debounce; POST `{message, messages[], watch, chat}` (`watch` = its name,
responsePolicy — missing means draft, never auto-send — instructions, skills;
every message carries `channel` and `chat`)
to each watch's hook (ADR 0027) with a bearer token read from an env var the
watch names, and an `Idempotency-Key` so a retried batch never runs twice.
Watches are registered through wfnexus (`wfx module watch add`, #103) and
read every tick, so a new watch needs no restart.

## Consequences

- One mental model: everything reusable is a task; a module just adds a
  version, a type contract and its requirements.
- The in-engine subscriptions + router (ADR 0026, #76) duplicated what a
  service module + hooks now do. Removed in #104: `wfx sub`,
  `/api/subscriptions`, the Subscriptions tab, the router and
  whatsapp-subscription-manager templates, `subscription_deliveries`, the
  channel `self` list and the `subscription` trigger kind. Conversations and
  `start:` completion events stay: they stand on their own.
- Supervision (#103): `wfx module start <m> --account a [--runs-on l]
  [--input k=v]` records one service per module+account in the state store
  (scope `service`). The server supervises those on labels it serves itself;
  `wfx-runner run` supervises those on its labels (`GET
  /api/workers/services`). Crashes restart with exponential backoff (1s → 5m,
  reset after a minute up). Every 15s the supervisor beats (`POST
  /api/workers/services/heartbeat`); the first owner holds the lease while it
  beats, a second machine is refused (409) and stops its copy. `wfx module ls`
  shows state (pending|running|crashed|silent), last heartbeat and restarts;
  no beat for 90s is `silent`, announced once on the default channel, and the
  lease is free for another worker.
- The service itself beats too. The supervisor starts it with `WFX_URL`,
  `WFX_SERVICE` and `WFX_SERVICE_TOKEN` (an HMAC of the key under the
  registration token: scoped to that one service, nothing stored). With these
  the service reads its watches (`GET /api/service-self/<m>/<account>/watches`)
  and beats from inside its loop (`POST …/heartbeat`). A process that is
  running but whose own beat is older than 90s (measured from its start) is
  hung, and shows `silent`, the same as a dead one. Watches are stored per
  service as `watch:<name>` (`wfx module watch ls|add|rm`); the monitor's
  `--watches` JSON file is gone.
- Not yet: carry delivery of `binary:` (manifest field only), trust/signing
  and permission diff on Sync (#85).
