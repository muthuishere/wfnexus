## Context

`contexts.json` already holds named servers with an optional SSH tunnel. `deploy.go` already plans
server and runner installs. Skill sources (`/api/skill-sources`) already import git repositories.
`mcp.json` is re-read every time a step builds its toolkit. carry is already driven by the server
(`/api/carry/*`) and versions every change it stores.

## Decisions

1. **A profile is a context.** Storage, the 0600 rule and the tunnel stay exactly as they were.
   `wfx profile` is the kubectl-shaped front door, and `wfx context` stays as an alias so no script
   breaks. Logging in by profile NAME (`wfx login --url prod`) now keeps that profile's URL, the
   same way a `--via` profile already did.
2. **Runners are not profiles.** A profile is something the CLI talks to, and the CLI never talks
   to a runner. Runner provisioning moves to `wfx worker install --to …`, beside `wfx workers`,
   which prints the join line.
3. **Login only follows a running server.** `--compose` and `--image` leave a server up, so the
   device login runs. A bare binary is installed but not started, and k8s needs a port-forward, so
   for those the command prints the next step instead of failing on a login.
4. **One mechanism per origin of a skill.** Git URL → skill source (pinned ref, refresh, clashes
   reported). Name or path → `<workdir>/installed-skills/<name>`, a registry root placed after the
   machine's roots and before git sources, so installing never changes what an existing name
   resolves to. The frontmatter `name:` must match, because the registry keys skills by it.
5. **MCP credentials are names.** `envFrom: [NAME]` is stored, and `resolveMcpEnv` reads the value
   from the env store (system scope) or the process environment when the toolkit is built. A name
   set nowhere fails the step and names the key. A credential is never started silently missing.
6. **Versioning is one snapshot file.** Putting every installed-skill file into carry separately
   would make one `wfx skill add` many carry versions. One JSON snapshot at
   `<workdir>/packages.snapshot.json` keeps it at one version per change. The rollback route
   restores it when the state rolled back is the server's own.
7. **Hot reload by polling.** A fingerprint (mtime+size) of the catalog inputs every 2 s adds no
   dependency, works on NFS, overlay and ConfigMap symlink swaps, and costs a few stats.
   `ReloadDefinitions` already builds new registries and swaps the pointers under the lock, and a
   bad load keeps the old catalog. A running step keeps the toolkit it built, and the next step
   sees the new catalog. The snapshot is per step, not per run (see Risks).
8. **Tools on PATH** are carry's: the server image starts as `carry run -- wfx-server`, and carry
   applies a new version on its poll. wfnexus resolves binaries per call, so a new tool needs no
   reload. Workers already pick up their state on their own `carry run` poll; nothing reaches into
   a worker.
9. **Windows installer is `.cmd`.** curl.exe, certutil and reg ship with Windows 10 1803+. Release
   assets are bare `.exe`, so `tar` is not needed. The user PATH is read from and written to
   `HKCU\Environment` with `reg`, not `setx %PATH%`, which would merge the system PATH into the
   user's and truncate it at 1024 characters. A throwaway `setx` broadcasts the change.

## Risks

- A run whose later steps start after a reload sees the new catalog for those steps. Per-run
  pinning would need the catalog threaded through the run; deferred.
- A rollback done by carry's own poll (not through `wfx config rollback`) rewrites the snapshot file
  on disk but does not restore it. Only the API route restores. Deferred: watch the snapshot file.
- A batch PATH entry containing `!` is mangled under delayed expansion. This is rare and
  documented in the script.
- install.cmd has not been run on Windows from this machine. CI's windows-latest job runs it
  against the latest release.
