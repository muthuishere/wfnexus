# Options: one-line server deploy (#115)

Status: options, no code. Date: 2026-11-08.

## The problem

Upgrading the server on `wfnexus-db` needs a sudo password. Today it runs `/usr/local/bin/wfx-server`
under a **system** unit (`/etc/systemd/system/wfnexus.service`), user `wfnexus`, sqlite, config in
`~/.config/wfx`. The same host runs the crypto desk and other prod services, so a deploy must touch
only wfnexus's own files and its own unit.

## What already exists (we build on it)

`.github/workflows/release.yml` already publishes, for each tag:
- release tarballs `wfx-server-linux-{amd64,arm64}.tar.gz` plus `checksums.txt`;
- images `ghcr.io/muthuishere/wfx-server:<v>` and `wfx-runner:<v>`;
- a Helm chart.

So every option below only ships **already-published artifacts**. None of them needs a new build pipeline.

## Deploy steps every option shares

1. Fetch the artifact (a tarball or image of `--version vX`, or a `--local-build`).
2. Back up the sqlite DB (`sqlite3 .backup`, not `cp` on a live DB) and `~/.config/wfx`.
3. Swap the release and restart the service.
4. Health check: `/api/health` returns 200, the reported version matches, and the model is ready. Wait up to 60 s.
5. If the health check fails, put the previous release back, restart, and exit non-zero.

---

## Option A: `wfx server deploy` over ssh, with a systemd `--user` unit and linger (recommended)

**One-liner**
```sh
wfx server deploy prod                 # latest release
wfx server deploy prod --version v0.9.3
wfx server rollback prod
```
`prod` is a named target in the wfx config: `{driver: ssh, host: wfnexus-db}`.

**Bootstrap (once, needs sudo once)**
- `wfx server deploy prod --migrate-system-unit` prints, then runs over an interactive ssh (`ssh -t`):
  `sudo loginctl enable-linger wfnexus`, `systemctl disable --now wfnexus.service`, and it moves the
  unit file aside. Every later deploy runs without sudo.
- The new layout is under the user's home:
  `~/.local/share/wfnexus/releases/<v>/`, `current -> releases/<v>`, and the unit
  `~/.config/systemd/user/wfnexus.service`, whose `ExecStart` points through `current/bin/wfx-server`.
- Ports above 1024 need nothing special. If the server binds below 1024, keep a reverse proxy in front.

**Rollback**: repoint `current` at the previous release and run `systemctl --user restart`. The last 3
releases and DB backups are kept, so rollback takes seconds and needs no download.

**Workers**: `wfx runner deploy <target>` is the same driver with the `wfx-runner` tarball and a
`wfx-runner.service` user unit. Many hosts are a list in the target: `{hosts: [w1, w2]}`, deployed one at a time.

**Effort**: about 2–3 days. The ssh driver uses the ssh binary the owner already has, so `~/.ssh/config` aliases just work.
It needs the upload/backup/swap/health/rollback steps, the target config, `status`, and the one-time migration.

**Failure modes**
- Linger is not enabled: the unit dies on logout or reboot. Bootstrap checks for this, and so does `status`.
- `systemctl --user` over ssh needs `XDG_RUNTIME_DIR`. Run `export XDG_RUNTIME_DIR=/run/user/$(id -u)`.
- The DB schema has migrated forward, so rolling back the binary is not enough. Rollback restores the backup taken
  *before* the swap. Writes made since then are lost, and the CLI says so before it restores.
- The disk is full: check free space before the upload.
- The model is not ready in time: there is a separate timeout, and `--no-wait-model` skips that check.

**How it extends**: `Driver` interface: `Upload/Backup/Swap/Health/Rollback/Status`. The ssh driver is the
first implementation. docker (B) and k8s (E) are later drivers behind the same command.

---

## Option B: docker compose over ssh

**One-liner**: `wfx server deploy prod-docker --version v0.9.3`. The driver runs
`ssh host 'cd ~/wfnexus && WFX_VERSION=v0.9.3 docker compose up -d'`.

**Bootstrap**: docker installed on the host and the `wfnexus` user in the `docker` group. Membership of the
`docker` group is root-equivalent, on a box that also runs the crypto desk. That is a real security cost.
The sqlite DB and config would move into a volume.

**Rollback**: deploy the previous tag. Images stay cached locally, so it is fast.

**Workers**: compose services, `wfx-runner` replicas.

**Effort**: about 2 days. A compose file already ships in the air-gapped tarball.

**Failure modes**: docker daemon upgrades restart every container on the box. Bind-mount permissions break
on uid changes. The host also needs ghcr pull auth if the image is ever private.

**How it extends**: it is the `docker://` driver of Option A.

---

## Option C: Kamal

**One-liner**: `kamal deploy` (or `wfx server deploy` shelling out to it).

**Bootstrap**: Ruby/kamal on the owner's machine, docker on the host, a `deploy.yml`, kamal-proxy, and a
registry login. Kamal assumes it owns docker on the host and wants root, or the docker group, there.

**Rollback**: `kamal rollback <version>`. It is good and built in.

**Workers**: Kamal "roles" (`servers: { web: [...], runner: [...] }`). This is a clean fit.

**Effort**: about 1 day of config. But it is a second tool, a second config, and a second mental model next to `wfx`.

**Failure modes**: kamal-proxy takes ports 80/443, which can clash with what the shared host already runs.
Health checks need `/up` or extra config. The sqlite volume needs to be pinned to a single host.

**How it extends**: it does not extend to k8s or to bare systemd. It would sit beside `wfx`, not inside it.

---

## Option D: GitHub release plus self-update, `wfx-server upgrade`

**One-liner**: `ssh wfnexus-db wfx-server upgrade --version v0.9.3`. Or the server checks for new
releases itself, and an admin triggers the upgrade from the UI.

**Bootstrap**: the binary sits in a user-writable directory, with the same migration to a user unit as Option A.
After that it can replace itself.

**Rollback**: the binary keeps `wfx-server.prev`, and `wfx-server upgrade --rollback` restores it.

**Workers**: each runner upgrades itself, or the server tells runners to upgrade. Doing that safely is fiddly.

**Effort**: about 2 days. The server needs GitHub API access and checksum verification.

**Failure modes**
- A process replacing its own binary and restarting itself under systemd has edge cases: half-written
  files, and a restart loop if the new binary crashes on start.
- The server needs outbound access to GitHub.
- A server that can upgrade itself is an attack surface.

**How it extends**: it only covers bare-binary installs. It is useful later as the step that A's ssh driver
runs *on the host*, but it is not the right first step.

---

## Option E: k8s, with `kubectl set image` or `helm upgrade`

**One-liner**: `wfx server deploy k8s-prod --version v0.9.3`, which runs
`helm upgrade wfnexus oci://ghcr.io/... --set image.tag=0.9.3 --wait`.

**Bootstrap**: a cluster and a kubeconfig context. The chart already exists. sqlite needs a single-replica
StatefulSet with a persistent volume (PVC), or a move to Postgres.

**Rollback**: `helm rollback wfnexus` (or `kubectl rollout undo`).

**Workers**: a `wfx-runner` Deployment, with replicas scaled through `--set runner.replicas`.

**Effort**: about 1 day for the driver. Running the cluster is far more effort, and nobody runs one today.

**Failure modes**: PVC access-mode and node-affinity problems with sqlite. Image pull secrets. Probes misconfigured.

**How it extends**: it is the `k8s://` driver behind the same command. Build it when there is a cluster to deploy to.

---

## Comparison

| | Sudo per upgrade | New tool | Shared-host risk | Rollback | Effort | Extends to others |
|---|---|---|---|---|---|---|
| **A ssh + systemd --user** | none (once at bootstrap) | none | lowest: touches only the user's home | symlink flip, seconds | 2–3 d | it *is* the driver frame |
| B compose over ssh | none | docker | docker group = root | prev tag | 2 d | as a driver |
| C Kamal | none | kamal + docker | proxy takes 80/443, docker root | built in | 1 d + ops | no |
| D self-update | none | none | the server holds GitHub access | `.prev` binary | 2 d | no |
| E helm/k8s | n/a | cluster | n/a | helm rollback | 1 d + cluster | as a driver |

## Decision (owner, 2026-11-08)

**Option A, ssh only.** Docker and k8s users already have their own upgrade tools (`docker compose pull`,
`helm upgrade`, Kamal, Argo...). Our job there is to keep publishing a versioned image
(`ghcr.io/muthuishere/wfx-server:<v>`, `wfx-runner:<v>`) and the Helm chart. That already happens on every tag.
We do **not** build docker, k8s or Kamal drivers. Options B, C and E are dropped. D is dropped too.

`wfx server deploy` therefore has one job: putting a release tarball on a bare host over ssh, with no sudo.

Scope:
1. `wfx server deploy|status|rollback <target>`, ssh only. The laptop pulls the GitHub release, copies it to
   the host, backs up the DB, swaps the `current` link, restarts with `systemctl --user`, health-checks, and
   rolls back automatically if the check fails.
2. One-time migration of `wfnexus-db` from the system unit to a user unit with linger. This is the only sudo, and the owner runs it.
3. `wfx runner deploy <target>`, on the same code path.
