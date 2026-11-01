## Why

The owner redefined the client's surface: "wfx server install should not be wfx install server —
wfx profile switch server like kubectl, and wfx profile create or install." A server is something you
switch between, so getting one is part of creating a profile. Getting skills and MCP servers onto a
server should use the same noun-verb form as `wfx tool` (a generic `wfx install <pkg>` "looks odd").
Each change should be recorded carry-style and picked up without a restart. Installing the CLI
should also install the agent skills by default, and Windows gets a `.cmd` installer in place of
`install.ps1`.

This supersedes the `wfx install server|runner` part of `one-client-install-to-config`. That
change's carry verbs (`wfx tool`, `wfx config`, `wfx sync`) are kept unchanged.

## What Changes

- **`wfx profile`** (kubectl-style, stored in the same `contexts.json`):
  - `create <name> --url U [--via ssh://…]` registers a server.
  - `create <name> --install local|ssh://…|k8s [--compose|--image] [--version v] [--dry-run]
    [--no-login]` provisions the server with the existing deploy plans (install.sh, docker compose
    from PR #64, the GHCR image, helm), then registers the profile, switches to it and logs in when
    the server is left running.
  - `use|switch`, `ls`, `current`, `rm`.
  - `wfx context …` stays as an alias.
- **`wfx worker install --to local|ssh://…|k8s`** provisions a runner. Profiles are servers only,
  because nothing talks to a runner.
- **`wfx install server|runner` is removed** and now says where it went. `wfx install --skills|--list`
  is unchanged.
- **`wfx skill add <name|git-url|path> | rm | ls`**: a git URL becomes a skill source (the existing
  `/api/skill-sources`); a name (a skill this server ships) or a local path (uploaded) goes into a
  server-owned `installed-skills` root. `wfx skills …` (plural, skill sources) keeps working.
- **`wfx mcp add <name> --set k=v… --env NAME… | rm | ls`** writes the server's `mcp.json`. `--env`
  stores an env-store key NAME (`envFrom`), and the value is injected when a step builds its toolkit.
  A literal that looks like a credential is refused, and no error message echoes a value.
- **Carry-style versioning**: after every skill/MCP change, the server puts ONE snapshot file (MCP
  config, skill-source list, installed skills) into its own carry state. `wfx config log` lists it,
  and `wfx config rollback <n>` restores it to disk and reloads. With carry off, the change applies
  and the response says it was not versioned.
- **Auto-reload**: a catalog watcher polls a fingerprint of registries.json, mcp.json, the
  skill-source list and every skill root (`WFX_CATALOG_POLL`, default 2 s, 0 = off) and calls
  `ReloadDefinitions` on change. Writes through the API reload immediately.
- **Installers**: `install.sh` runs `wfx install --skills` after installing the `wfx` CLI (not for
  wfx-server or wfx-runner). It prints one line saying how many skills and where, and opts out with
  `WFX_SKILLS=0` or `--no-skills`. If the skills step fails it warns and the install still succeeds.
  `install.cmd` replaces `install.ps1` and uses only curl.exe, certutil and reg. The `wfx` binary
  falls back to its embedded skills, so a downloaded binary can install them.
- Authz: writes to `/api/skills/installed` and `/api/mcp/{name}` need `admin:write`, because they
  put code the server runs, the same reach as carry.

## Impact

- `cmd/wfx`: `profile.go`, `skillmcp.go` (new); `deploy.go` (runDeploy split, PR #64 compose),
  `workers.go`, `skillinstall.go`, `contexts.go`/`login.go` (log in by profile name), usage text.
- `internal/engine`: `packages.go`, `snapshot.go`, `watch.go` (new); one more skill root; `envFrom`
  resolution in `buildToolkit`. `internal/catalog`: `McpServer.Environment/EnvFrom`, `SaveMcp`,
  `DeleteMcp`. `internal/api`: `packages.go`, rollback restores packages, authz override.
- `install.sh`, `install.cmd` (new), `install.ps1` (deleted), CI job for install.cmd on windows-latest.
- No migration and no new dependency.
