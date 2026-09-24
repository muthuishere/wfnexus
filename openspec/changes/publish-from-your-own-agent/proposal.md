## Why

The product claim is that you build a workflow inside the agent you already use — Claude Code,
Codex, opencode — and publish it to a server you host. Today the second half does not exist: every
asset (skills, MCP declarations, workflows, templates, providers) is read from disk at boot and
nothing can be pushed. There is also no identity, so there is nobody to publish *as*, and
`wfx` talks to exactly one host named by a single `WFX_API` environment variable.

That gap is what makes the current answer to "how do I get my workflow onto the server" be "copy
the file there yourself", which is not a product. It also blocks the one hard gate in
`docs/not-now.md`: auth before this is reachable on any address that is not localhost.

## What Changes

- **`wfx login --url <host>`** — authenticate with a server using the OAuth 2.0 Device
  Authorization Grant (RFC 8628), the flow `gh auth login`, Docker and the AWS CLI use. No
  localhost callback and no browser on the box, so it works over SSH, in a container, and inside
  someone else's agent session — which is the point, because authoring happens inside the user's
  own agent.
- **`wfx logout`**, and **named host contexts** so several servers live side by side
  (`gh auth login --hostname` / `kubectl config` / `docker login <registry>`). Host-agnostic by
  construction: "host it wherever you like" becomes another context rather than a feature.
- **`wfx publish`** — push a workflow to a host as a **bundle**: its skills and MCP declarations
  are resolved at publish time and travel with it, content-addressed. A workflow alone is not
  runnable on the receiving machine, and an unknown skill name is a boot error, so references
  resolved at pull time would make "portable" mean "portable if the destination already agreed".
- **Users, roles and a default admin**, all editable. Authentication is pluggable later (OIDC,
  LDAP); **authorization is ours, permanently**, and project-scoped from day one.
- **Every API route requires a subject** when the server is not bound to loopback. The existing
  bearer extraction on worker routes becomes server-wide middleware resolving either a machine
  identity (a worker) or a person (a user token).
- **BREAKING (deployments only):** the default bind address becomes `127.0.0.1:8090` instead of
  `:8090`. Already landed; containers and k8s manifests state `WFX_ADDR=:8090` themselves, so no
  deployment changes. A single-machine install keeps **no auth at all** on loopback.
- **NOT in this change:** OIDC/LDAP, token refresh and rotation, signing of published bundles,
  dependency resolution between assets, and a public registry. Named so they are not assumed.

## Capabilities

### New Capabilities

- `identity`: who a request is from. Device-grant login, user tokens, logout, named host
  contexts in the client, and the rule that a subject is required off-loopback and absent on it.
- `authorization`: what a subject may do. Roles, a default admin, project scoping, and the
  requirement that authorization is resolved by us and never delegated to an identity provider.
- `asset-publishing`: pushing a workflow and its dependencies to a host as an immutable,
  content-addressed bundle, and what is refused (a literal credential, an unresolvable skill).

### Modified Capabilities

<!-- None. openspec/specs/ is empty: this is the first change, so every capability here is new.
     The behaviour this change alters (the bind default, the un-authenticated API) has no spec
     to delta against yet. -->

## Impact

- `apps/api/cmd/wfx/main.go` — `call()` sends no `Authorization` header and reads a single
  `WFX_API`; needs a contexts file and a bearer on every request.
- `apps/api/internal/engine/workers.go:62-90` — `newToken()`/`HashToken()` are already the
  mint-and-hash path a user token should reuse. They live in `engine` under a worker-only name,
  so the first real refactor is a rename and move, not new crypto.
- `apps/api/internal/api/workers.go:25` — bearer extraction exists on worker routes only; becomes
  server-wide middleware.
- `apps/api/internal/model/` — no user, role or session types exist. `ScopeProject` already
  exists, so project scoping has a hook.
- `apps/api/migrations/` + `apps/api/migrations/sqlite/` — paired migrations for users, roles,
  tokens and published bundles.
- `apps/api/internal/skills/` — skill resolution becomes a publish-time operation as well as a
  boot-time one. Note `skills/platform.go` states a platform tool never writes; `wfx publish` is a
  CLI verb run by a person, not a tool handed to a step, and that boundary must hold.
- `apps/api/internal/catalog/save.go` — `looksLikeSecret` already refuses a literal credential on
  save; publishing must apply the same rule.
- `docs/adr/0017-identity-is-the-device-grant.md`, `docs/adr/0018-the-registry-has-a-publish-direction.md`
  — the decisions this change implements.
- Distribution is a hard prerequisite and is **not** covered here: there are no tags, no CI, no
  goreleaser and no install path, so today nobody can obtain the client this change is about.
