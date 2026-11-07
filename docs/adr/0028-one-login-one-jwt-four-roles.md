# 0028 — One login, one JWT, four roles

Status: proposed · 2026-11-07 · issue #96

Amends ADR 0017 (identity), ADR 0022 (browser sessions) and ADR 0025
(password sign-in). Keeps ADR 0027 (hook/MCP exposure tokens) unchanged.

## Context

Today a person holds an opaque `wfx_…` bearer that never expires: the CLI's
device grant mints one, the browser's cookie is one, an admin can mint one.
Every request hashes it and looks it up. Roles are `admin`, `publisher`,
`viewer`, all global: there is no way to say "Ana may edit THIS workflow".

The owner asked for it to be simple: one login, one credential shape used by
the UI, the CLI and the API, and four roles.

## Decision

1. **One access credential: a JWT.** HS256, 15 minutes, claims
   `sub` (user id), `name`, `sid` (the refresh row), `iat`, `exp`. The signing
   key is derived (HMAC-SHA256, label `wfx-jwt-v1`) from the env-store key
   that already exists (`WFX_SECRET_KEY` / the key file, `internal/secrets`);
   no new secret to manage. A server with no env-store key signs with a random
   per-process key, so its sessions end on restart.
2. **One refresh token**, the existing `wfx_…` value stored hashed in
   `user_tokens` (label `refresh`). `POST /api/auth/refresh` trades it for a
   fresh JWT. Revoking the row (logout, admin) ends the session; the JWT
   carries `sid`, and the middleware checks the row still exists, so a revoke
   takes effect on the next request rather than in 15 minutes. A refresh
   token is NOT accepted as a bearer.
3. **Every login ends the same way.** `POST /api/auth/password`, the device
   grant (`/api/device/token`) and the browser grant all issue
   `{access_token: <jwt>, refresh_token, expires_in}`. The browser gets the
   JWT in the `wfx_session` cookie and the refresh value in `wfx_refresh`
   (both HttpOnly, SameSite=Strict); when the JWT cookie has expired the
   middleware refreshes it transparently. `wfx login` stores both in the
   0600 contexts file and refreshes before expiry. `wfx login --user NAME`
   signs in with a password (read from `WFX_PASSWORD` or stdin).
4. **Four roles.** Global: `superadmin` (`*`), `editor` (edit/run/publish
   every workflow — the old publisher), `user` (read, run what it can see,
   and see only its own runs). Per workflow, in `workflow_members`:
   `owner` (edit, run, approve, expose, and choose who may edit) and
   `editor` (edit, run, approve). A member grant ADDS to the global role for
   that one workflow; it never removes. Migration renames
   `admin→superadmin`, `publisher→editor`, `viewer→user` in both `roles` and
   `users`.
5. **Unchanged:** loopback = no auth (ADR 0017); hook and MCP exposure
   tokens (ADR 0027) and worker tokens stay machine credentials; legacy
   `wfx_…` user tokens minted before this change keep working as bearers
   until revoked, so existing CLI contexts and `WFX_API_TOKEN` steps do not
   break.

## Consequences

- One paired migration (Postgres 000026, SQLite 000020): role renames, the
  `user` role gains `runs:write`, `editor` gains the new `runs:all`
  (see every run; without it a caller sees runs it started plus runs of
  workflows it holds a grant on), and the `workflow_members` table.
- Starting a run (`POST /api/workflows/{name}/runs|dispatches`) now needs
  `runs:write`, not `workflows:write`: running is not editing.
- Rotating `WFX_SECRET_KEY` now also signs everyone out (and still loses
  every sealed env value, as before).
- "Shared" for a `user` means "visible in its project scope". Explicit
  per-workflow sharing to users is left for later; the member table can
  carry a `runner` role if it is wanted.
- No UI page for workflow members yet: the API is
  `GET|PUT|DELETE /api/workflows/{name}/members[/{user}]`.
