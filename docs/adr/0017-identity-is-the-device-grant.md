# ADR 0017 — Identity is the device grant

- **Status:** proposed
- **Date:** 2026-09-24

## Context

`docs/not-now.md` carries one hard gate. Every other row on that table is a
judgement call waiting for something to go wrong; the auth row says **"the
moment this is exposed on any address that is not localhost … it is not 'when
it hurts', it is 'before anyone else can reach it'."** This ADR is what closes
that gate.

The pivot of 2026-09-24 brings the gate forward. The product is a self-hosted
registry for agent assets, and `wfx publish` needs an identity to publish *as*
— which is why login is ordered before publish despite "skill first". The
client is now the product surface.

The constraint that decides the flow: under this shape, authoring happens
**inside the author's own Claude Code or Codex session**. There is no browser on
that box, and often no box — it is an SSH session, a container, or somebody
else's agent running on somebody else's machine. A localhost callback has
nowhere to land.

We are not starting from nothing. `internal/engine/workers.go` already mints
`wfx_…` tokens: a machine runs one join command, presents the registration
token, gets its own token back, and polls with it. The value is never stored —
only `HashToken`'s SHA-256 — and rotation invalidates the join command without
disturbing machines already joined.

## Decision

**`wfx login --url https://wfx.example.com` uses the OAuth 2.0 Device
Authorization Grant (RFC 8628).** The flow `gh auth login`, `docker login` and
`aws sso login` use. Adopt it; do not invent one.

The device grant specifically, and not a redirect flow, because the client
prints a code and polls — no callback port, no browser on the box. That is what
makes it work over SSH, inside a container, and inside someone else's agent
session, which is the whole point.

- `wfx logout`.
- **Multiple hosts are named contexts**, side by side — `gh auth login
  --hostname`, `kubectl config`, `docker login <registry>`. The client is then
  host-agnostic by construction, so "host it wherever you like" is another
  context rather than a feature anyone has to build.
- **Authentication is pluggable**: local users first, OIDC and LDAP later.
- **Authorization is ours, permanently**, and never delegated to the identity
  provider. It is **project-scoped from day one** — `model.ScopeProject` already
  exists and a run already belongs to a project. A token that can push to one
  repository has no business reaching another's workflow.
- **Bootstrap is a default admin user and a default set of roles, both
  editable.** Not a fixed role set baked into the binary.

### How this relates to the worker token

It is the same token model with a second subject, not a second subsystem.
A **worker token is a machine identity**: it is issued by a join, it carries
labels, and it answers "which machine may claim this job". A **user token is a
person**: it is issued by a device grant, it carries a role and a project scope,
and it answers "who ran this and who approved it". Both are bearer strings on
`Authorization: Bearer`, both are stored as `HashToken` and never in a log or an
event, and both are revocable without touching the other. The mint, hash and
revoke path is one path.

## The three-scale test

Every decision from here has to hold at all three, and this one is written to:

- **One person, localhost.** No auth at all. Nothing to log into, zero config,
  no default password to find. Unchanged.
- **A small org.** One shared server, `wfx login` on each laptop, a handful of
  users, roles, projects.
- **An enterprise.** OIDC or LDAP for authentication, our RBAC for
  authorization, an audit trail of who ran and who approved, air-gapped install.

**How the solo case stays free of it:** auth is **absent when the listener is
bound to loopback**, not merely permissive. The server decides this from its own
bind address, not from a setting: on loopback there is no identity in the
request, no anonymous user row, no implicit admin, and the authorization check
is not reached. The reachability boundary and the auth boundary are therefore the
same boundary — the inclusion rule `docs/not-now.md` closes on. Bind to anything
else and auth is required; there is no flag that turns it off, because a flag is
how a single-machine default becomes an exposed production install.

## Consequences

- Every `wfx` request grows a credential and a context to read it from. `call()`
  in `cmd/wfx/main.go` sends no `Authorization` header today, and `WFX_API` is a
  single env var — that is the shape that has to become a context file.
- `Addr` defaults to `":8090"`, which is every interface, not loopback. Under
  this decision that default is wrong twice over: it must be `127.0.0.1:8090`,
  or the rule above declares auth absent on a port the network can reach.
- Being pluggable costs an interface boundary now for a provider we do not yet
  have. That is deliberate: retrofitting OIDC behind hard-coded local users is
  the expensive version.
- **Deliberately not decided here:** token lifetime, refresh and revocation
  policy; the audit log schema (who ran, who approved, what it saw); SSO user
  provisioning and group-to-role mapping. Each is open, and each is a smaller
  question once the subject exists.
