# 0028 — One login, one JWT, four roles

Status: proposed · 2026-11-07 · issue #96

Amends ADR 0017 (identity), ADR 0022 (browser sessions) and ADR 0025
(password sign-in). Keeps ADR 0027 (hook/MCP exposure tokens) unchanged.

## Context

A person held an opaque `wfx_…` bearer that never expired. Roles were
editable rows of permission strings (`admin`, `publisher`, `viewer`), all
global: there was no way to say "Ana runs THIS workflow". There was one way
to sign in with a password and no way to use a company directory.

The owner asked for it to be simple: one login, one credential used by the
UI, the CLI and the API; exactly four roles; login providers that only
authenticate.

## Decision

### 1. One credential: a JWT plus a refresh token

- Access token: HS256 JWT, 15 minutes, claims `sub`, `name`, `sid`, `iat`,
  `exp`. The key is derived (HMAC-SHA256, label `wfx-jwt-v1`) from the
  env-store key (`WFX_SECRET_KEY` / the key file). With no env-store key, a
  random per-process key is used and sessions end on restart.
- Refresh token: a `wfx_…` value stored hashed in `user_tokens` (label
  `refresh:<how>`). `POST /api/auth/refresh` trades it for a new JWT. It is
  never accepted as a bearer.
- The JWT's `sid` names the refresh row, and the middleware checks that the
  row still exists. Logging out, disabling the user or revoking the row ends
  the session on the next request.
- Browser: JWT in `wfx_session`, refresh value in `wfx_refresh` (HttpOnly,
  SameSite=Strict). The middleware refreshes an expired session cookie in
  place, so the UI never handles a JWT. CLI: `wfx login` stores both tokens
  in the 0600 contexts file and refreshes before expiry.

### 2. Exactly four roles

| role | where it lives | reach |
|---|---|---|
| superadmin | `users.role = 'superadmin'` | everything |
| project admin | `members(project, P)` | P's workflows, members, settings (PATCH, env, expose) |
| workflow admin | `members(workflow, W)` | edit, publish, expose, approve, manage W's members |
| workflow runner | `members(workflow, W)` | run W, see its runs, answer its approvals |

- `users.role` holds `superadmin` or nothing. Roles are no longer editable:
  `GET /api/roles` lists the four, and `PUT`/`DELETE` answer 410.
- Anyone signed in may read the catalogue (workflows, registry, projects,
  bundles, state). Runs are filtered: a non-superadmin sees the runs it
  started plus the runs of the workflows and projects it holds a role on.
  Every write is checked in `internal/api/roles.go`, using only these four
  roles. Publishing a bundle, validating or dry-running a draft, and
  proposals are not addressed to one workflow, so they need an admin role
  somewhere. Deleting a project, users, registry, carry and workers are for
  superadmin only.
- Members: a project admin manages its project's members, and a workflow
  admin (or the project admin) manages the workflow's members.
  API: `GET|PUT|DELETE /api/{projects|workflows}/{name}/members[/{user}]`.
  CLI: `wfx member add|rm|ls USER --project P | --workflow W [--role R]`.
  Superadmin is set with `wfx user role NAME superadmin|none`.

### 3. Login providers authenticate only

The providers are configured in the server config file:

```yaml
auth:
  providers:
    - {name: corp, type: ldap, url: ldaps://ldap.example.com,
       bindDn: "cn=svc,dc=example,dc=com", bindPasswordEnv: LDAP_BIND_PW,
       baseDn: "dc=example,dc=com", userFilter: "(uid={username})",
       groupRoles: {ops: "workflow_runner:deploy"}}
    - {name: sso, type: oidc, issuer: https://idp.example.com,
       clientId: wfx, clientSecretEnv: SSO_SECRET}
    - {name: google, type: google, clientId: …, clientSecretEnv: GOOGLE_SECRET}
    - {name: github, type: github, clientId: …, clientSecretEnv: GH_SECRET}
    - {name: dev, type: mock-ldap, file: ./dev-users.yaml}
```

- `password` is the local users, as before. It is always available.
- `oidc` uses the authorization-code flow with discovery. The identity is
  read from the userinfo endpoint. `google` is an OIDC preset. `github` is an
  OAuth 2.0 preset that reads `/user`.
  The browser goes to `/api/auth/oauth/{name}/start`, and a state cookie
  guards the callback.
- `ldap`: a service bind, a search, then a bind as the user. Groups come from
  `memberOf`. `mock-ldap` is an in-process fake directory read from a YAML
  file, for dev and tests. It is refused off loopback unless `force: true`.
  Both go through `POST /api/auth/password {provider, username, password}`
  and `wfx login --user N --provider P`.
- Secrets are always given as the NAME of an env var, never as a value.
- An external person becomes the local user `<username>@<provider>`, so no
  provider can sign in as a local account. The first login creates that user
  with no roles. The exceptions are the provider's optional `defaultRole` and
  `groupRoles`, given as `superadmin`, `project_admin:P`,
  `workflow_admin:W` or `workflow_runner:W`. These apply only when the user
  is created. After that, roles are managed in wfnexus only.
- Every provider ends in the same session as in §1. A CLI on a
  redirect-only provider signs in with the device grant, approved from a
  browser signed in that way.

### 4. Unchanged

- Loopback means no auth (ADR 0017).
- Worker tokens and the hook/MCP exposure tokens (ADR 0027) stay machine
  credentials.

## Greenfield: no migration path

wfnexus has no existing users to preserve, so there is no compatibility
layer. A person authenticates only with a JWT; a `wfx_…` value is a worker
or exposure token, never a person's bearer. Creating a user mints no
credential. Old role names (admin, publisher, viewer) are not mapped.
The schema change is one paired migration (Postgres 000026, SQLite 000020)
that adds the `members` table, whose CHECK ties each role to its scope.

## Consequences

- Rotating `WFX_SECRET_KEY` also signs everyone out.
- The roles table is no longer consulted. It stays for the Roles page.
- No UI for members yet; the API and the CLI cover it.
- No Firebase.
