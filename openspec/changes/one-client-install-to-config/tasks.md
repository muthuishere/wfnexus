## 1. Server drives carry

- [x] 1.1 `internal/carryops`: run `carry --json`, parse the envelope, map error codes; env on stdin.
- [x] 1.2 `/api/carry/*` routes; `carry` → `admin:read|write` in `resourcePerms`.
- [x] 1.3 Off is a clear 409 (`CARRY_REPO`), a missing binary a 409 (`CARRY_BIN`).
- [x] 1.4 Test: a fake carry on `CARRY_BIN` records argv/stdin; an env value is never in argv; a listing has no values.

## 2. Client verbs

- [x] 2.1 `wfx tool add|rm|ls` with `--on`, `--state`, `--context|--to`.
- [x] 2.2 `wfx config put|get|ls|rm|log|rollback|status`, `wfx config env set|unset|ls` (names only).
- [x] 2.3 Usage lines in `wfx help` / `wfx help all`.

## 3. Move between places

- [x] 3.1 `wfx sync --from/--to` over contexts and `local:DIR` (files/ + env.defaults 0600), `--to-state`, `--only`, `--dry-run`.
- [x] 3.2 Test: a local → dev → prod (worker state) → local round trip against two fake servers.

## 4. Install

- [x] 4.1 `install.sh` `WFX_BIN` (allowlist).
- [x] 4.2 `wfx install server|runner --to local|ssh://|k8s [--image] [--version] [--set] [-f] [--dry-run]`.
- [x] 4.3 Test: the plan for each target; no secret value in a docker line.

## 5. Docs

- [x] 5.1 `site/src/content/docs/concepts/carry.mdx`: the wfx verbs, the install → config flow.

## 6. Follow-ups (not in this PR)

- [ ] 6.1 `wfx sync` workflows + skills (reuse `/api/bundles` publish/tar/install between contexts).
- [x] 6.2 Windows installer: superseded by `profiles-and-server-packages` (install.cmd replaces install.ps1).
- [ ] 6.3 `wfx install --build`: build from source (`task release`, or `docker build` of infra/Dockerfile*) instead of fetching.
- [ ] 6.4 Align `--set` keys with the helm chart once `infra/helm/wfnexus` lands; runner on k8s.
- [ ] 6.5 `--image` server in local mode (env/volume conventions). The compose-over-ssh path landed with `--compose` (PR #64, folded into `profiles-and-server-packages`).
- [ ] 6.6 Per-state tool listing (needs a carry `tool ls <state>` or a read of the state manifest).
- [ ] 6.7 A web UI page for carry. (Auto-registering after an install is `wfx profile create --install`, see `profiles-and-server-packages`.)
- [ ] 6.8 `wfx config mv` (carry `mv`) and `wfx config diff` (carry `diff`).
- [ ] 6.9 Live end-to-end against a real bucket (MinIO) with the real carry binary.
