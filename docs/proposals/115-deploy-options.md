# Options: one-line server deploy (#115)

Status: options, no code. Date: 2026-11-08.

## Decision already made (owner)

- **Deploy is ssh + systemd only.** `wfx server deploy <host>` targets bare Linux hosts.
- **Containers, Kamal and k8s get no driver.** We publish images and the chart, and document the tag bump.
  See the last section.

This doc picks *how* the ssh path upgrades without the sudo password that blocks us today.

## The host today (read-only inspection of `wfnexus-db`, 2026-11-08)

| | |
|---|---|
| Unit | **system** unit `/etc/systemd/system/wfnexus.service`, with `User=wfnexus` and `ExecStart=/usr/local/bin/wfx-server` |
| Env | `WFX_CONFIG=~/.config/wfx/config.yaml`, `EnvironmentFile=~/.config/wfx/env` and `~/.config/apl/master.env` |
| Limits | `MemoryMax=32G CPUQuota=600% Nice=10 IOWeight=50`. Production Postgres shares the box and always wins |
| Listen | `127.0.0.1:8090`, with health at `/api/health` |
| Data | `~/.local/share/wfnexus/` holds `wfnexus.db` (~63 MB, sqlite), `runs/`, `artifacts/`, `lanes/` and `defaults/`. Backups are taken by hand today (`wfnexus.db.bak-*`) |
| Arch | x86_64, user `wfnexus`, with `curl` and `sqlite3` present |
| **Linger** | **already `yes`** |
| User units | **already used for the other services** on this host: `apl-serve`, `chrome-agent-*`, `herdr-expose`, `nord-browser-proxy` |

The release already uploads a bare `wfx-server-linux-{amd64,arm64}` binary plus `checksums.txt`, so a deploy ships one file.

## Steps every option shares

These all run from the laptop, with `wfx server deploy wfnexus-db [--version vX | --binary ./wfx-server]`:

1. Resolve the version, either the latest GitHub release or `--version`. Download `wfx-server-linux-<arch>` (arch comes from
   `uname -m` over ssh) and verify it against `checksums.txt`. The laptop downloads it, so the server needs no GitHub access.
2. `scp` the file to the host.
3. Back up the DB with `sqlite3 wfnexus.db ".backup …"`, not `cp` on a live DB, and keep the last 5 backups.
4. Swap the binary and restart the service.
5. Health check: `curl 127.0.0.1:8090/api/health` must return 200 within 60 s. If it doesn't, restore the previous binary,
   restart, and exit non-zero.

`wfx server status <host>` and `wfx server rollback <host>` use the same pieces.
Workers use `wfx runner deploy <host>` with the `wfx-runner` binary and its own unit.

---

## Option 1: systemd `--user` unit plus a `releases/` symlink (recommended)

**Layout**
```
~/.local/share/wfnexus/releases/v0.9.3/wfx-server
~/.local/share/wfnexus/current -> releases/v0.9.3
~/.config/systemd/user/wfnexus.service      ExecStart=%h/.local/share/wfnexus/current/wfx-server
```
The user unit copies today's Environment, EnvironmentFile and limit lines. It drops `User=` and `Group=`, because a user unit
already runs as that user.

**Swap**: `ln -sfn releases/<v> current && systemctl --user restart wfnexus`.
**Rollback**: point `current` back at the previous release. The last 3 releases are kept, so there's no download.

**One time**: a single sudo session, `sudo systemctl disable --now wfnexus.service`, followed by the first deploy.
Linger is already on, so that's all.

**For**
- No root is ever needed again.
- It matches how every other service on this box already runs.
- Rollback just points `current` back at an older release, so it's instant.

**Against / failure modes**
- `systemctl --user` over a non-login ssh needs `XDG_RUNTIME_DIR=/run/user/$(id -u)`, which the driver sets.
- Cgroup limits in a user unit only apply to the controllers delegated to user units. Systemd 244+ delegates cpu,
  memory and io by default, but the first deploy should verify that `MemoryMax`/`CPUQuota` show up in
  `systemctl --user show wfnexus`. Postgres priority depends on them.
- The cut-over from the system unit to the user unit is ~1 restart of downtime, and it happens once.

**Effort**: about 2 days.

## Option 2: keep the system unit and add a one-time sudoers rule

**One time**: install a root-owned helper `/usr/local/sbin/wfnexus-swap <file>`. It verifies the file, copies it to
`/usr/local/bin/wfx-server` (keeping `.prev`) and runs `systemctl restart wfnexus`. Then add
`wfnexus ALL=(root) NOPASSWD: /usr/local/sbin/wfnexus-swap` to `/etc/sudoers.d/wfnexus`.

**Swap and rollback**: run `sudo -n wfnexus-swap ~/upload/wfx-server`, or `--rollback` to put `.prev` back.

**For**: nothing about the running unit changes, so it's the smallest change to prod today.
**Against**
- Root is still in the loop, and the helper is a root-run script that must be written and reviewed carefully.
  Wildcard sudoers rules on `install`/`cp` would be a root hole, which is why a fixed helper is needed instead.
- Only one previous binary is kept.
- Every new host needs the sudoers file. That makes setup per host heavier.

**Effort**: about 2 days, plus a careful security review of the helper.

## Option 3: self-update, `wfx-server upgrade`

The server downloads its own new binary, verifies it, replaces itself and exits, and systemd restarts it.
**Against**
- The binary has to sit in a user-writable path, which means the same unit migration as Option 1 anyway.
- The server needs outbound GitHub access.
- A server that can rewrite itself is an attack surface.
- Rolling back when the new binary crash-loops needs a supervisor outside the process, which is Option 1 again.

**Effort**: about 2–3 days, and it buys nothing over Option 1.

---

## Recommendation: Option 1

It is the only option where root disappears for good, and it is how this host already runs `apl-serve`, `chrome-agent`
and `herdr-expose`. Linger is already enabled, so the only sudo ever needed is one `disable --now` of the old unit.
Releases and backups sit beside the data, so rollback needs no download.

Build order:
1. `wfx server deploy|status|rollback <host>`.
2. The owner runs the one-time cut-over on `wfnexus-db`, with Postgres-friendly limits verified.
3. `wfx runner deploy <host>`.

## Containers, Kamal, k8s: bump the tag

We do not build drivers for these. Each release tag already publishes:
- `ghcr.io/muthuishere/wfx-server:<v>` and `wfx-runner:<v>`, plus the floating tags `:<major.minor>` and `:latest`;
- the Helm chart.

To upgrade, change the tag in your own file:

| You use | Upgrade |
|---|---|
| Dockerfile `FROM` | `FROM ghcr.io/muthuishere/wfx-server:0.9.3`, then rebuild |
| docker compose | `image: ghcr.io/muthuishere/wfx-server:0.9.3`, then `docker compose pull && docker compose up -d` |
| Kamal | `image:` / `KAMAL_VERSION`, then `kamal deploy` (`kamal rollback <v>` to undo) |
| Helm | `helm upgrade wfnexus <chart> --set image.tag=0.9.3` (`helm rollback wfnexus` to undo) |

Back up the DB volume before a version that changes the schema. Pin an exact tag in prod, not `:latest`.
