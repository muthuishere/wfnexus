# 0030: Tenants and service principals

Status: proposed · 2026-11-16 · issue #160

## Context

Every run, published bundle and credential on a server shares one namespace. A service that calls this server needs a credential that reaches only its own runs and workflows. Nothing in the request must be able to widen what it reaches.

## Decision

1. **A tenant is a boundary that projects belong to.** A project belongs to at most one tenant. A project with no binding belongs to `default`. Runs and published bundles carry a `tenant_id`, written from their project's binding at insert. An install with one owner has one tenant, `default`, and behaves as before.
2. **Binding a project moves its rows.** Binding moves the project's existing runs and bundles in the same transaction. Moving a bound project to another tenant is refused.
3. **Tenant-scoped reads are fail-closed.** A read requires the row's `tenant_id` and its project's current tenant to both match. A run written concurrently with a bind is then hidden from every tenant, never shown to the wrong one.
4. **A service principal is a machine bound to exactly one tenant.**
   - Its token is `wfxs_` plus 256 random bits, shown once. Only the sha256 is stored, and lookup is one statement on the hash that excludes revoked rows.
   - The tenant a request acts in is read from the stored principal, never from the request.
5. **A principal reaches `/api/svc/` and nothing else.**
   - The `authorize` middleware refuses it on every other route, matching on the escaped path as the router does.
   - Every `/api/svc/` handler refuses any other subject, including a superadmin and an anonymous loopback caller: there is no tenant to act in.
   - Another tenant's resource answers exactly as an absent one does.
6. **A principal starts only workflows an operator lists for it**, and only while the workflow's project is in its tenant. Every refusal is the same 404 and creates no run.
   - Starting runs this way ships switched off, and no configuration switches it on. It is turned on only after write steps always pause for approval and the engine hands over write inputs (#146, #148).
7. **A declared surface is a reference only.** A principal can record the endpoint its tools are reached at and their names. Nothing is called through it here.
8. **Administration is a superadmin's.** On loopback, administration is open, as for users. A worker token is refused, although the route table lets a worker through.

## Consequences

- Isolation is enforced by the application. Neither SQLite nor the Postgres schema adds row-level security.
- A superadmin, a worker and a loopback caller remain cross-tenant on the existing routes. The routes for people are not tenant-aware yet.
- Project and workflow names stay global. Two tenants cannot both have a project with the same name.
- The migration is numbered 000032 (SQLite 000026), above the numbers that open changes use. It must be applied after them, because golang-migrate never goes back to fill a lower version.
- Two refusals sit behind earlier gates and cannot be seen failing on their own, so they are redundant second walls:
  - `can()` refusing a service subject, behind `authorize`;
  - the `tenants` route-table permission, behind the handler's operator check.
