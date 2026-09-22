# ADR 0020 — Authentication and tenancy

- **Status:** accepted
- **Date:** 2026-09-22

## Context

There is none. Every endpoint is open, every run is global, and anyone who can
reach `:8090` can start a workflow that executes shell commands with the
server's credentials against a repository of their choosing.

That is the correct shape for a single-developer tool on localhost, which is
what this has been. It is disqualifying for anything else, and **nothing else on
the roadmap should ship publicly before it** — a sandbox (0016) and an egress
policy (0017) protect the host from the workflow, and neither protects the
platform from an unauthenticated stranger.

The repository is going public. Public code with no auth story invites someone
to run it as-is on a public address.

## Decision

**Every request is authenticated, and every row belongs to a tenant.**

- **Two credential kinds, one identity model.** A human session (browser, from
  the React UI) and an API token (`wfx`, CI, another service). Both resolve to
  the same `(tenant_id, actor_id, scopes)` before any handler runs, so an
  endpoint never learns which kind it was.
- **Tenancy is a column, enforced at the query, not in the handler.** Every
  table that holds a run, a step, an event, an artefact or a provider carries
  `tenant_id`, and the store's API takes the tenant as an argument that cannot
  be omitted — not a value read from a context that a new code path might forget
  to set. The mistake this is guarding against is the one ADR 0004 names in a
  different domain: a parameter that expresses intent is not a control.
- **Scopes are coarse and few**: `workflows:read`, `runs:write`,
  `runs:approve`, `admin`. Approval is separated from execution deliberately,
  because the approval gate is the only human check in a workflow that writes
  code, and an actor that can start runs should not be able to self-approve
  them.
- **Artefacts are served through the API, never by a signed bucket URL.** A
  MinIO URL that leaks is a tenant boundary crossed with no audit trail.
- **`wfx` gets a token, not the database.** The CLI is an API client (ADR 0013)
  and this keeps it one; a CLI that could reach Postgres directly would bypass
  every rule above.

## What this refuses to build

**No user management, no roles, no SSO, no invitations.** A tenant is a row and
a token; who administers it is somebody else's system. Those are product
features, and building them here would embed an organisation model in a platform
that does not need one yet.

**Localhost is not exempt.** A "trusted local mode" that skips auth is how the
open default survives into production. Instead, first boot mints a token and
prints it once, and `wfx` stores it — one step of friction, no branch in the
authorisation path.

## Consequences

- Every existing endpoint, the React app and `wfx` all change at once. There is
  no incremental path: a half-authenticated API is an unauthenticated API with
  extra code.
- The migration has to place existing rows in a tenant. There is one operator
  and one tenant today, so it is a default — recorded here so it is a decision
  rather than a surprise in a migration file.
- Audit becomes possible and therefore expected: a run records who started it
  and who approved it. The events table already exists to hold that.
