# Design — publish from your own agent

## Context

The server reads everything it knows off a disk at boot: `skills.DefaultRoots`
(`apps/api/internal/skills/registry.go:45-53` — project dir, `~/.claude/skills`,
`~/.agents/skills`, first root wins, later ones recorded as `Shadowed`),
`registries.json` via `catalog`, `templates/`. There is no inbound direction.

The client is one file. `base()` (`apps/api/cmd/wfx/main.go:156-162`) reads a single
`WFX_API` env var defaulting to `http://127.0.0.1:8090`; `call()` (`:163-193`) sets
`Content-Type` and nothing else — **no `Authorization` header is sent by any verb**.
The verb table is a flat `switch` at `main.go:52-105`.

The credential machinery we need already exists, under a worker-only name.
`newToken()` (`apps/api/internal/engine/workers.go:82-86`) mints `wfx_` + 24 random
bytes hex; `HashToken()` (`:90-93`) is SHA-256 hex and its comment already states the
value is never written to a database, a log or an event. `api.bearer()`
(`apps/api/internal/api/workers.go:25-27`) extracts the bearer, and `authWorker`
(`:30-38`) resolves it — but only on the four worker routes. Every other route under
`r.Route("/api", …)` (`apps/api/internal/api/api.go:72-…`) is unauthenticated.

`cfg.Addr` already defaults to `127.0.0.1:8090` (`apps/api/internal/config/config.go:190`)
and is passed to `http.Server` at `apps/api/main.go:212`. `api.New` does **not** receive
it — that is a real gap this design has to close.

A blob layer already exists: `blob.Store` with `Put`/`Get`/`PresignedGet`
(`apps/api/internal/blob/blob.go:19-24`), two drivers — MinIO/S3 and `Folder`
(`folder.go`), the latter writing via temp-file + `os.Rename` and refusing keys that
escape the directory (`folder.go:47-56`). Nothing about bundle storage needs inventing.

This design implements ADR 0017 (identity is the device grant) and ADR 0018 (the
registry has a publish direction). It does not relitigate either.

## Goals / Non-Goals

**Goals:**

- A concrete RFC 8628 device grant: endpoints, code shape, polling, expiry.
- A client contexts file with a stated precedent, permissions and selection rule.
- One token model, two subject kinds, reusing `newToken`/`HashToken` verbatim.
- A loopback exemption that is a property of the code, not of a setting.
- A bundle format built on the existing `blob.Store`, content-addressed.
- Publish-time refusals: literal credential, unresolvable skill, digest mismatch.

**Non-Goals** (named so they are not assumed — proposal "NOT in this change"):
OIDC/LDAP, token refresh/rotation policy, bundle signing, dependency resolution
between bundles, a public registry, blob garbage collection, the audit-log schema.
Distribution of the client (tags, CI, goreleaser) is a prerequisite and out of scope.

## Decisions

### 1. The device grant, concretely

**Adopted whole: OAuth 2.0 Device Authorization Grant, RFC 8628.** No invented flow.
Two endpoints plus one browser page, all on the wfx server (it is both authorization
server and resource server here; separating them is what OIDC later does for us).

| endpoint | RFC 8628 | what it does |
|---|---|---|
| `POST /api/device/code` | §3.1–3.2 | client posts `client_id=wfx-cli`; server returns `device_code`, `user_code`, `verification_uri`, `verification_uri_complete` (§3.3.1), `expires_in`, `interval` |
| `POST /api/device/token` | §3.4–3.5 | client polls with `grant_type=urn:ietf:params:oauth:grant-type:device_code` and the `device_code`; answers `authorization_pending`, `slow_down`, `expired_token`, `access_denied` (§3.5) or the token |
| `GET /device` (UI) | §3.3 | the page the human opens: enter the code, see who is asking, Approve / Deny |

- **`user_code`**: 8 characters from the §6.1 recommended base-20 alphabet
  (`BCDFGHJKLMNPQRSTVWXZ` — no vowels, so no words; no digits, so no 0/O or 1/I),
  displayed `XXXX-XXXX`. §6.1 gives this 20^8 ≈ 2.56e10; §5.2 requires rate limiting
  on the verification endpoint, so the user-code entry form is limited to 5 attempts
  per minute per source and the code is burned after 5 total failures.
- **`interval`: 5 seconds**, RFC 8628 §3.2's default. The client honours `slow_down`
  by adding 5s (§3.5) and never polls faster than the last returned interval.
- **`expires_in`: 900** (15 minutes). Long enough to walk to another machine, short
  enough that an abandoned code on a shared terminal is worthless. RFC 8628 gives no
  number; 15 min is `gh`'s order of magnitude (unverified — not read from `gh` source).
- **The `device_codes` row is deleted on success, denial or expiry.** It is
  single-use by construction (§3.5: the device code MUST NOT be reused).
- **What the browser page shows**: the client's name (`wfx-cli`), the hostname it was
  invoked from, the requested scope, and the user the browser is signed in as. Approve
  binds *that* browser session's user to the device code. This is the only place the
  human authorizes; the CLI never sees a password.
- **Client-side after approval**: only the token and the context metadata (below).
  No refresh token — refresh is explicitly deferred, so the access token is
  long-lived and revocable by the server, exactly as worker tokens are.

*Unverified:* whether the existing UI router can host `/device` outside `/api` without
disturbing the SPA fallback in `api.go`; the fallback's shape was not read.

### 2. Where the client keeps contexts

**Precedent taken: `gh` (`~/.config/gh/hosts.yml`) — a single file, keyed by host,
holding the token itself.** Not `docker login` (it delegates to an OS credential
helper, which is a second component to ship on three platforms and a support burden
for a v1) and not `kubectl` (its file describes clusters, users and namespaces as
three cross-joined lists; we have one axis).

```
~/.config/wfx/contexts.json          # 0600, parent dir 0700
{
  "current": "wfx.example.com",
  "contexts": {
    "wfx.example.com": {
      "url": "https://wfx.example.com",
      "token": "wfx_…",
      "user": "muthu",
      "project": "acme/api",
      "createdAt": "2026-09-24T…Z"
    }
  }
}
```

- **The file holds a credential, so it is `0600` and its directory `0700`**, written
  with the temp-file + rename pattern `blob/folder.go:65-80` already uses. The client
  **refuses to read a contexts file whose mode is group- or world-readable** and says
  so, the way `ssh` refuses a loose private key. A warning is not a mitigation.
- **Resolution order in `base()`**, most specific first:
  1. `--url <host>` on the command line — selects the named context, or errors if
     there is no context for that host (`wfx login --url` first).
  2. `WFX_API` — **kept, unchanged**, so every existing script and the localhost case
     keep working. A `WFX_API` with no matching context sends no bearer, which is
     exactly right for loopback (§4).
  3. `contexts.current`.
  4. `http://127.0.0.1:8090`.
- `call()` gains one line: if the resolved context has a token, set
  `Authorization: Bearer <token>`. Nothing else in the transport changes.
- `wfx logout [--url]` calls `DELETE /api/tokens/self`, then removes the context.
  Removing the local entry without revoking server-side leaves a live credential.

### 3. Token model

**Nothing new is minted.** `newToken` and `HashToken` move from
`apps/api/internal/engine/workers.go` to a neutral package (`internal/auth`), keeping
the `wfx_` prefix and the SHA-256 storage rule; `engine` imports them. The first
commit of this change is a rename and a move, not crypto.

One table, two subject kinds:

```sql
-- apps/api/migrations/0000NN_identity.up.sql      (Postgres)
-- apps/api/migrations/sqlite/0000NN_identity.up.sql (same columns, this dialect)
create table if not exists users (
    id            uuid primary key,
    name          text        not null unique,
    display_name  text        not null default '',
    role          text        not null,            -- names a row in roles
    disabled      boolean     not null default false,
    created_at    timestamptz not null default now()
);
create table if not exists roles (
    name        text primary key,
    permissions text[] not null default '{}'
);
create table if not exists user_tokens (
    id          uuid primary key,
    user_id     uuid not null references users(id) on delete cascade,
    token_hash  text not null unique,   -- HashToken only. The value is never stored.
    label       text not null default '',
    project     text not null default '',  -- '' = every project the role allows
    created_at  timestamptz not null default now(),
    last_used   timestamptz
);
create table if not exists device_codes (
    device_code text primary key,      -- hashed, same rule as a token
    user_code   text not null unique,
    approved_by uuid references users(id),
    status      text not null default 'pending',  -- pending|approved|denied
    attempts    int  not null default 0,
    expires_at  timestamptz not null
);
```

`workers` keeps its own `token_hash` column (`migrations/000005_workers.up.sql`).
A worker token and a user token are **separate tables on purpose**: revoking every
user token must not disturb a machine mid-job, and a worker has labels where a user
has a role and a project. What is shared is the mint/hash/compare path and the
`Authorization: Bearer` wire format — ADR 0017's "one path", read as one *code* path,
not one table.

Authorization differs and does not converge: a worker answers *may this machine claim
this job* (label match, `engine.AuthWorker`); a user answers *may this person do this
to this project* (role permission + `project` scope, hooked to `model.ScopeProject`,
`apps/api/internal/model/model.go:129`). Both are `store` lookups by `HashToken(bearer)`.

**Bootstrap**: on first boot with a non-loopback bind and no users, the server creates
`admin` and prints a one-time device-grant-free bootstrap code to stdout (the server
log, which only the operator reads). Roles `admin`, `publisher`, `viewer` are seeded
as **rows**, editable — ADR 0017 forbids a role set baked into the binary.

### 4. The loopback exemption

The failure mode being prevented, named: **an operator sets `WFX_ADDR=:8090` in a
container (the manifests already do), no users exist, and the server serves an
unauthenticated API to the network** — a config mistake that silently reopens the one
hard gate in `docs/not-now.md`.

Making it a property of the code:

1. **`api.New` takes `addr string`.** Today it does not (`api.go:51`), which is the
   whole gap — the handler cannot currently know how it is reachable.
   `apps/api/main.go:212` already has `cfg.Addr` in hand and passes it.
2. At construction, `api.New` computes `loopbackOnly := isLoopback(addr)` **once**,
   from the bind address, and installs `r.Use(s.requireSubject)` on the `/api` router
   **unconditionally**. There is no branch that omits the middleware. `isLoopback`
   splits host/port and returns true only for `127.0.0.0/8`, `::1` or `localhost`; an
   empty host (`:8090`) is **not** loopback.
3. `requireSubject` resolves the bearer to a subject (user or worker) and puts it on
   the request context. On failure it returns 401 — **unless** `loopbackOnly`, in
   which case it sets a nil subject and continues. Authorization checks read the
   subject; a nil subject on a loopback server means the check is not reached.
4. **No anonymous user row and no implicit admin.** ADR 0017 says absent, not
   permissive. A nil subject is representable; a "local" user is not, so there is no
   principal to accidentally grant off-loopback.
5. **There is no flag.** No `WFX_AUTH=off`, no `--no-auth`. The only way to get the
   exemption is to bind to loopback, so the reachability boundary and the auth
   boundary are the same boundary — the inclusion rule ADR 0017 asks for.
6. **Boot refusal for the named failure mode.** Non-loopback bind + zero rows in
   `users` ⇒ the server prints the bootstrap admin credential and refuses to serve
   until it is claimed, rather than serving openly. This is the one behaviour a
   careless `WFX_ADDR=:8090` must not be able to produce.

Two honest gaps. (a) A reverse proxy in front of a loopback bind re-exposes an
unauthenticated API; that is the operator's own tunnel and no bind-address rule can
see it — it is documented, not defended. (b) `127.0.0.1` is shared by every local
user and every process on the box, so "loopback" means "this machine is the trust
boundary". That is the single-machine premise, stated rather than assumed.

The worker routes keep `authWorker` as a second, narrower check; the middleware does
not replace it, it precedes it.

### 5. The bundle format

**A published bundle is a gzipped tar with a JSON manifest at its root**, addressed by
content digest the way an OCI image manifest addresses its layers (ADR 0018). OCI's
*vocabulary* is adopted — manifest, layer, `sha256:` digest, immutable version, moving
tag. The OCI *distribution API* is not, because we are not serving container runtimes
and a `/v2/` endpoint set would be a large surface for one client.

```
manifest.json                      # the manifest (below)
workflow.yaml                      # the workflow as published
skills/<name>/…                    # each skill directory, verbatim
mcp.json                           # the MCP declarations the steps name
```

**Digest of what, exactly.** Two levels:

- **Entry digest** — `sha256:` of the bytes of a single file, or, for a skill (a
  directory), of a canonical listing: each file's relative slash-path and its content
  digest, sorted by path, one `"<digest>  <path>\n"` line each. That listing is stored
  in the manifest, so a skill's digest is reproducible from its contents alone and
  does not depend on tar ordering, mtimes or permissions.
- **Bundle digest** — `sha256:` of `manifest.json`'s canonical bytes (sorted keys, no
  insignificant whitespace). The manifest names every entry digest, so the bundle
  digest covers the whole bundle transitively. This is the OCI arrangement.

**Where the bytes go: the existing `blob.Store`.** No new storage. Key layout
`bundles/<sha256>/<mediatype>` — content-addressed, so a laptop gets files under the
`Folder` driver and a deployment gets objects in a bucket, and the choice is already
one line of config (`blob/folder.go:15-24`). `Folder.pathFor` already refuses a key
that escapes the directory; digests are hex, so no key we generate can. The database
holds only the index:

```sql
create table if not exists published_bundles (
    id            uuid primary key,
    kind          text not null,        -- skill|mcp|workflow|template|provider
    project       text not null,
    name          text not null,
    version       text not null,        -- semver
    digest        text not null,        -- sha256:… of the manifest
    manifest      jsonb not null,
    published_by  uuid references users(id),
    published_at  timestamptz not null default now(),
    unique (project, kind, name, version)
);
```

**Republish of the same name.** `unique (project, kind, name, version)` makes
`name@version` twice a **refusal, not an overwrite** — npm's rule, for npm's reason
(ADR 0018). A `latest` tag is a separate `bundle_tags` row pointing at a version; a
tag moves, a version never does. Republishing *identical bytes* is still refused: the
error message says the version exists and shows that the digest matches, so the
author knows it is a no-op rather than a conflict.

**Resolving a bundle back into a runnable workflow.** On pull (or on a server-side
run of a published workflow), the server materialises the bundle into a
**bundle-scoped skill root** — a directory containing exactly that bundle's skills —
and prepends it to `skills.DefaultRoots`. First-root-wins
(`skills/registry.go:4-5`) then resolves every step's `skills:` to the carried copy,
and any same-named machine skill is recorded in `Shadowed` rather than silently
winning. That is the existing resolution rule used, not bypassed: it is *why* bundling
works. ADR 0018's point stands here — resolution at pull time would let a workflow
mean something different on two machines because the roots differ.

`skills/platform.go:18` says a platform tool never writes. That boundary holds:
materialisation is done by the server on a CLI-initiated publish/pull, never by a tool
handed to a step. No new platform tool is added by this change.

### 6. What publishing refuses

Publish is the gate. Failing here rather than at run time on someone else's machine is
the entire point of the verb — a workflow that arrives and then fails to load is
exactly the "portable if the destination already agreed" outcome ADR 0018 rejects.

1. **A literal credential.** `catalog.looksLikeSecret` (`apps/api/internal/catalog/save.go:77-…`,
   called at `:31` and `:52` — over 64 characters, or any byte outside `[A-Za-z0-9_]`)
   is applied unchanged to every `apiKeyEnv` in a published provider or classifier and
   to step env keys, the rule `workflow/env.go` already applies. Same function, same
   message: *apiKeyEnv must be the NAME of an environment variable, not a value.*
   The check runs **client-side before upload and server-side on receipt** — the
   client so nothing leaves the machine, the server because a client is not a guard.
2. **An unresolvable skill or MCP reference.** Every name a step lists must resolve
   against the publisher's roots at publish time and be carried in the bundle. An
   unresolved name is a publish error naming the skill and the roots searched — the
   same loud failure `skills` already produces at boot, moved to where it is cheap.
3. **A digest mismatch.** The server recomputes every entry digest and the manifest
   digest from the received bytes. A mismatch is a 400, the bundle is not indexed, and
   nothing is written under the claimed key. Content addressing is worth nothing if
   the store accepts bytes on the uploader's word.

Refused ⇒ nothing is stored: index row and blob writes are one transaction, blobs
first (a blob with no index row is an orphan, which GC — deferred — collects; an index
row with no blob is a broken bundle).

## The three-scale test

**One person, localhost.** Zero config, no login, no default password. `cfg.Addr` is
already `127.0.0.1:8090` (`config.go:190`), so `loopbackOnly` is true, `requireSubject`
passes a nil subject, and there are no user rows at all. **Publishing is absent, not
merely unused**: `wfx publish` with no context and no `--url` is an error — "no host:
run `wfx login --url …`" — not a degraded local mode that writes into `skills/`.
Files on disk keep working exactly as they do today. `WFX_API` still works untouched.

**A small org.** One shared server, `WFX_ADDR=:8090`, an admin created at first boot,
`wfx login --url` on each laptop, roles and projects as rows. Same binary, same
`blob.Store` (probably still `Folder` on one box). The device grant is what makes this
work from inside an SSH session or a container, where a redirect flow has nowhere to
land (RFC 8628 §1).

**An enterprise.** Air-gapped: the bundle is a tar and the store is a bucket, so the
whole registry is copyable offline; nothing in publish reaches the internet. Their own
registry: `--url` is a context, so there is no notion of *the* registry. Provenance:
`published_bundles.published_by` and `published_at` record who published what, in
git's sense of an author — signing is explicitly deferred (ADR 0018, "not decided
here"), so this design must not be read as claiming tamper-evidence. Authentication
becomes OIDC/LDAP behind the same subject interface; **authorization stays ours**
(ADR 0017) — the identity provider says who, the `roles` table says what.

## Risks / Trade-offs

- **A reverse proxy in front of a loopback bind serves an unauthenticated API** → not
  defendable from the bind address. Documented in the deployment notes; the boot log
  states in one line whether auth is active.
- **`user_code` brute force** → RFC 8628 §5.2 rate limiting, 20^8 space (§6.1), 5
  failures burns the code, 15-minute expiry.
- **A long-lived token with no refresh** → deliberate (refresh is out of scope). The
  mitigation is revocation: the hash is a row, `wfx logout` deletes it, and an admin
  can delete any. Token lifetime policy is ADR 0017's own open question.
- **Bundle duplication and staleness** → the honest price ADR 0018 accepts, the one
  npm pays with a lockfile. Each carried skill's digest is recorded so a pull can say
  *this bundle carries `fix-author@sha256:…` and you have a different one.*
- **Unreferenced blobs accumulate** → no GC in this change; content addressing means
  they are at worst duplicated, never wrong.
- **`api.New` gains a parameter** → one call site (`apps/api/main.go:212`). Cheap, and
  the alternative (a package-level flag) is precisely the settable switch §4 refuses.

## Open Questions

- Does the SPA fallback in `api.go` permit a non-`/api` `/device` route? Unverified.
- Does the UI need its own session (cookie) to approve a device code, or does the
  first admin approve via a bootstrap code? Both are sketched above; only one survives.
- The permission vocabulary in `roles.permissions` is unnamed here on purpose — ADR
  0017 requires roles be editable rows, and the verb list should follow the routes
  that exist, which the specs artifact enumerates.
- Whether `wfx pull` is part of this change or waits. The proposal names only
  `publish`; resolution-on-pull is designed above but the verb is not claimed.
