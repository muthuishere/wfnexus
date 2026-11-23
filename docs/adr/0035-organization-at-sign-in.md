# 0035: An OIDC sign-in places a person in one organization, with an operator-mapped role

Status: proposed · 2026-11-23 · issue #188 · amends 0017 and 0030

## Context

Tenants (ADR 0030) hold runs, bundles and service principals, but a person signed in here was never in one: `Subject.Tenant` was set only for service and account subjects. One server behind one identity provider could therefore serve one organization only.

An identity provider that serves many organizations already says which one a person belongs to, in a claim of the id_token (Microsoft Entra's `tid`, for example), and often what they are there (an app-role claim).

ADR 0017 says authorization is never delegated to the provider: a group claim or a role in a token is never read as a permission. That rule stands. What this ADR adds is how a claim may take part without breaking it.

## Decision

1. **Opt-in, per provider.** An `oidc` or `google` provider may set `orgClaim`, `roleClaim`, `roleMap`, `allowedOrgs`, `autoCreateOrg` and `orgPolicy`. A provider without `orgClaim` changes nothing: its people stay unscoped (`Tenant ""`), with the roles they had. The settings other than `orgClaim` are refused without it, and `orgClaim` is refused on a provider that issues no id_token (github, ldap).

2. **The claims come from the verified id_token only**, never from userinfo or anything in the request. The org claim must be a string of at most 256 bytes; the role claim may be a string or an array of strings.

3. **A token value is only a lookup key; the operator's config decides.** `roleMap` is written by the operator and maps a role-claim value to `org-admin` or `member`. A value not in the map, or no role claim, is `member`. The provider never names a role that is granted as is, so ADR 0017's rule holds: the decision is local config, re-read at every sign-in. This amends 0017 only in allowing a claim to SELECT among roles the operator declared.

4. **The tenant id is always derived: `org-` + the first 32 hex characters of sha256(provider + NUL + claim)** (`store.OrgTenantID`). The claim is never the id. So:
   - no claim can name an existing tenant (`default`, or one the operator made for something else) and join it;
   - the same claim on two providers is two tenants, so one identity provider cannot speak for another's organizations;
   - no literal claim can equal another claim's derived id.

   The readable claim is kept only as the tenant's name, with unprintable characters dropped (`store.OrgTenantName`). An operator who wants a tenant to exist before anyone signs in (with `autoCreateOrg` off) creates it under `OrgTenantID(provider name, claim)`.

5. **One tenant per person, stored server side** (`user_tenants`). The first sign-in binds; a later one naming another organization is refused, not re-bound; the role (only ever `org-admin` or `member`) is refreshed from `roleMap` at every sign-in. A missing claim is refused; with `allowedOrgs` set, any organization not listed is refused before anything is created. An organization not yet a tenant is created when `autoCreateOrg` is on and refused when it is off. Creating the tenant and binding are one transaction, and an account the sign-in made is deleted if the binding fails, so a refused sign-in leaves neither an orphan account nor an orphan tenant. Every refusal is written to the audit log, claim values and names quoted.

6. **`Subject.Tenant` for a person comes only from that row** (with `Subject.OrgRole`), read on every request, whatever the credential (browser session, refresh, CLI token). A failure to read it fails the request rather than unscope the person. A person whose provider sets `orgClaim` and who has no row is refused (401) rather than served unscoped; signing in again binds them. A superadmin is always unscoped (`Tenant ""`, `OrgRole ""`), whatever a row made before their promotion says.

7. **`orgPolicy`**: `guarded` (the default) holds an organization's people to the customer authoring guard; `full` does not. Anything but an explicit `full` on the person's provider, including a provider no longer configured, is guarded.

8. **Off until scoping is enforced.** An organization's people must not get an unscoped view of the server or its shell steps. Until tenant-scoped visibility and the guard are wired into the routes (#189), every provider that sets `orgClaim` is refused when the providers are built, behind one switch in the api package (`orgScopingEnforced`).

   #189 wires both and turns the switch on. The browser API answers 404 for anything of another tenant through one decision, `tenantVisible` (internal/api/tenancy.go): an organization's person sees their tenant only; a person in no organization sees the default tenant (every unbound project, so everything on an install without organizations); a superadmin sees every tenant. An org-admin administers the tenant's projects without member rows; a member may create projects (bound to the tenant, stored as `<tenant>--<name>` and shown short) and run and save the tenant's workflows; a grant never reaches across the boundary. Under `orgPolicy: guarded`, saves, runs, dispatches, dry runs, validation and copies go through the customer authoring guard (no `run:` steps, no tools, no triggers on save). Global state, channel adapters, module installs and registry writes stay superadmin-only.

## Consequences

- One server can serve many organizations behind one identity provider, once #189 lands.
- **An organization's roles come only from `roleMap`.** `defaultRole` and `groupRoles` grant server roles: superadmin, which sees across every tenant, or a project or workflow role that may sit in another tenant. A provider that sets `orgClaim` is refused when the providers are built if it sets either.
- **An existing superadmin is never bound.** An account that already holds superadmin signs in through an organization provider unbound, with an audit line. An ordinary account that existed before `orgClaim` was set is bound on its next sign-in, and from then on is guarded like any other member.
- **Moving a person out of an organization** is an operator action, never a sign-in: delete their row AND revoke their tokens, in one call (`store.UnbindUserTenant`). Their next sign-in binds them from the claim.
- **`autoCreateOrg` without `allowedOrgs` is open.** On an identity provider that serves many organizations, any organization whose people can sign in to the app registration creates a tenant here. List the organizations in `allowedOrgs`, or leave `autoCreateOrg` off and create tenants yourself.
- **Multi-tenant Microsoft Entra.** Each Entra tenant issues id_tokens under its own issuer (`https://login.microsoftonline.com/<tid>/v2.0`), and the id_token check here is strict: the issuer must equal the configured one. One provider therefore serves one Entra tenant's issuer; several Entra tenants need one provider each (and so, by decision 4, get distinct tenants here even for equal claims) until a templated-issuer check is designed.
