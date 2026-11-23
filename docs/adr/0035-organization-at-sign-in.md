# 0035: An OIDC sign-in places a person in one organization, with an operator-mapped role

Status: proposed · 2026-11-23 · issue #188 · amends 0017 and 0030

## Context

Tenants (ADR 0030) hold runs, bundles and service principals, but a person signed in here was never in one: `Subject.Tenant` was set only for service and account subjects. One server behind one identity provider could therefore serve one organization only.

An identity provider that serves many organizations already says which one a person belongs to, in a claim of the id_token (Microsoft Entra's `tid`, for example), and often what they are there (an app-role claim).

ADR 0017 says authorization is never delegated to the provider: a group claim or a role in a token is never read as a permission. That rule stands. What this ADR adds is how a claim may take part without breaking it.

## Decision

1. **Opt-in, per provider.** An `oidc` or `google` provider may set `orgClaim`, `roleClaim`, `roleMap`, `autoCreateOrg` and `orgPolicy`. A provider without `orgClaim` changes nothing: its people stay unscoped (`Tenant ""`), with the roles they had. The settings other than `orgClaim` are refused without it, and `orgClaim` is refused on a provider that issues no id_token (github, ldap).

2. **The claims come from the verified id_token only**, never from userinfo or anything in the request. The org claim must be a string; the role claim may be a string or an array of strings.

3. **A token value is only a lookup key; the operator's config decides.** `roleMap` is written by the operator and maps a role-claim value to `org-admin` or `member`. A value not in the map, or no role claim, is `member`. The provider never names a role that is granted as is, so ADR 0017's rule holds: the decision is local config, re-read at every sign-in. This amends 0017 only in allowing a claim to SELECT among roles the operator declared.

4. **The tenant id is derived, never trusted as is.**
   - A claim value that is already a valid tenant id is used unchanged. It is not lowercased, so `Acme` and `acme` stay apart.
   - Any other value becomes `org-` plus 32 hex characters of its sha256.
   - A claim naming `default` is refused: that tenant owns every unbound project.

5. **One tenant per person, stored server side** (`user_tenants`). The first sign-in binds; a later one naming another organization is refused, not re-bound; the role (only ever `org-admin` or `member`) is refreshed from `roleMap` at every sign-in. A missing claim is refused. An organization not yet a tenant is created when `autoCreateOrg` is on and refused when it is off. A refused first sign-in makes no account, and every refusal is written to the audit log.

6. **`Subject.Tenant` for a person comes only from that row** (with `Subject.OrgRole`). A failure to read it fails the request rather than unscope the person.

7. **`orgPolicy`**: `guarded` (the default) holds an organization's people to the customer authoring guard; `full` does not. Anything but an explicit `full` on the person's provider, including a provider no longer configured, is guarded.

8. **Off until scoping is enforced.** An organization's people must not get an unscoped view of the server or its shell steps. Until tenant-scoped visibility and the guard are wired into the routes (#189), every provider that sets `orgClaim` is refused when the providers are built, behind one switch in the api package (`orgScopingEnforced`).

## Consequences

- One server can serve many organizations behind one identity provider, once #189 lands.
- **No superadmin through an organization provider.** A superadmin sees across every tenant, so a provider that sets `orgClaim` is refused when the providers are built if its `defaultRole` or any `groupRoles` value grants superadmin. Its other mapped roles still apply.
- **An existing superadmin is never bound.** An account that already holds superadmin signs in through an organization provider unbound (Tenant ""), with an audit line. An ordinary account that existed before `orgClaim` was set is bound on its next sign-in, and from then on is guarded like any other member.
- Moving a person to another organization is an operator action (delete the row), never a sign-in.
