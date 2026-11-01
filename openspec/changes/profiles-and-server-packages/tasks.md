## 1. Profiles

- [x] 1.1 `wfx profile create|use|switch|ls|current|rm` over contexts.json; `wfx context` kept as alias.
- [x] 1.2 `create --install` reuses deploy plans (incl. PR #64 `--compose`), registers, logs in when the server is running.
- [x] 1.3 `wfx login --url <profile-name>` keeps that profile's URL.
- [x] 1.4 `wfx worker install --to …`; `wfx install server|runner` removed with a pointer.
- [x] 1.5 Tests: create/switch/current/rm, context alias, dry-run registers nothing, old spelling refused.

## 2. Skills and MCP on the server

- [x] 2.1 Engine: installed-skills root, InstallSkill (frontmatter name, no path escapes, atomic swap), RemoveInstalledSkill.
- [x] 2.2 Catalog: McpServer.Environment/EnvFrom, SaveMcp/DeleteMcp, credential-literal refusal.
- [x] 2.3 envFrom resolved in buildToolkit from env store / process env; a missing name fails, naming it.
- [x] 2.4 API: `/api/skills/installed`, `PUT|DELETE /api/mcp/{name}`, admin:write.
- [x] 2.5 CLI: `wfx skill add|rm|ls` (git URL → skill source; name; path upload), `wfx mcp add|rm|ls`.

## 3. Versioned, live

- [x] 3.1 Snapshot (mcp.json, skill-source list, installed skills) put into the server's carry state after each change.
- [x] 3.2 Rollback of the server's state restores the snapshot and reloads.
- [x] 3.3 Catalog watcher (`WFX_CATALOG_POLL`), started by the server.
- [x] 3.4 Tests: live install/remove, snapshot→restore round trip, watcher on a hand edit, envFrom.

## 4. Installers

- [x] 4.1 install.sh: skills by default for `wfx`; `WFX_SKILLS=0` / `--no-skills`; warn-not-fail.
- [x] 4.2 `wfx` falls back to its embedded skills, so a downloaded binary can install them.
- [x] 4.3 install.cmd (curl.exe, certutil, reg) replaces install.ps1; CI job on windows-latest.

## 5. Docs

- [x] 5.1 `concepts/profiles.mdx` (new), `concepts/carry.mdx`, quickstart, README; `wfx help` text.

## 6. Follow-ups (not in this PR)

- [ ] 6.1 Per-run catalog pinning (today: per step).
- [ ] 6.2 Restore the snapshot when carry's own poll applies a rollback (watch the snapshot file).
- [ ] 6.3 `wfx skill add <name>` from a remote registry/catalogue, not only what the server ships.
- [ ] 6.4 Header values by env-store reference for remote MCP servers (today: envFrom → process env only).
- [ ] 6.5 Redact MCP-injected values from run events the way step env secrets are.
- [ ] 6.6 Server settings hot-reload (`wfx config env set` reaches the server at its next start).
- [ ] 6.7 Run install.cmd on a Windows ARM64 machine.
