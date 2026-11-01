## Why

Today a wfnexus install is three tools: `install.sh` (or an image, or nothing yet for k8s) to get the
server, the separate `carry` CLI (holding the bucket's S3 credentials and the state key on the
operator's laptop) to put tools, config files and env defaults on the server and its workers, and
`wfx` for everything else. The owner's ask: "carry to install and configuration, move here and there,
local to server, all with the client". One client, against the current context, for the whole
lifecycle: get it there, configure it, move configuration between places.

## What Changes

- **`wfx install server|runner --to local|ssh://user@host|k8s`** reuses what exists: `install.sh`
  (checksum-verified; a new `WFX_BIN` picks `wfx-server`/`wfx-runner`, the assets
  `ship-a-gettable-binary` already publishes), the GHCR images (`--image`, `docker run` here or over
  ssh, with configuration passed by NAME such as `-e CARRY_REPO`, never a value), or `helm upgrade
  --install` of `infra/helm/wfnexus` (the chart belongs to another change; we only shell out).
  `--dry-run` prints the plan. The existing `wfx install --skills|--list` is unchanged.
- **The server drives carry**: `/api/carry/*` (tools add/rm/catalog, files ls/get/put/rm, env
  names/set/unset, log, rollback, states, status), authorized as `admin:read|write`. The server runs
  its own `carry --json` (the frozen v0.1 CLI contract), so the client needs only its wfx token. Env
  values go to carry on stdin, never argv. Carry off (`CARRY_REPO` unset) is a 409 that says how to
  turn it on; a missing binary is a 409 naming `CARRY_BIN`.
- **`wfx tool add|rm|ls`**, **`wfx config put|get|ls|rm|log|rollback|status`**,
  **`wfx config env set|unset|ls`** act on the server's state by default, or `--on <worker>` (a
  worker's state is its name) or `--state s`. `--context`/`--to`/`--from` picks another context.
- **`wfx sync --from <ctx|local:DIR> --to <ctx|local:DIR>`** moves config files and env defaults
  local → server, server → local, and server → another server (and between states with
  `--to-state`). It is additive, takes `--dry-run` and `--only files,env`, and does NOT move secrets,
  which live in the env store.
- Docs: `concepts/carry.mdx` shows the wfx verbs first; raw `carry` stays for publishing tools.

## Impact

- `apps/api/internal/carryops` (new), `internal/api/carry.go` (+ one authz row),
  `cmd/wfx/{carry,deploy}.go`, `install.sh` (`WFX_BIN`), the docs page. No migration, no new
  dependency.
- Aligns with `a-runner-sets-itself-up`: nothing reaches INTO a worker. The server writes the bucket,
  and the worker's own `carry run` pulls on its next poll.
- Workflows and skills in `wfx sync`, `install.ps1`, build-from-source and helm values are follow-ups
  (tasks.md §6).
