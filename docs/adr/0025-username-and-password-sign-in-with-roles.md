# ADR 0025 — Username and password sign-in, with users and roles managed in the UI

- **Status:** proposed
- **Date:** 2026-11-05
- **Amends:** ADR 0022 (the browser signs in through the terminal). The device grant stays; a password form is added beside it.
- **Keeps:** ADR 0017's bind-address rule (loopback = no auth; off loopback = auth required, first boot prints an admin credential)

## Context

The owner asked for wfnexus to be "simple — just username, password and roles".

Today (checked on main, 2026-11-05):
- Sign-in is a device grant only: `wfx login --url` and `wfx login --approve <code>`. The browser gets a cookie only after a signed-in terminal approves it (ADR 0022). There is no password form and no stored password hash.
- Roles are table rows, seeded on first boot as `admin` (`*`), `publisher` and `viewer` (`internal/api/authz.go` `DefaultRoles`). Permissions are read on every request, so a role change takes effect on the next request.
- There is no `wfx user …` command and no Users page in the UI.

A team member without a terminal session cannot get into the UI at all. That person is exactly who uses the messaging templates (ADR 0024).

## Decision

1. **Password sign-in.** `POST /api/auth/password {username, password}` checks the user's password hash (argon2id) and sets the same `wfx_session` cookie ADR 0022 defines. That gives the same token table, the same revocation and the same CSRF rules. The UI shows a plain sign-in form. The device grant stays for the CLI and for headless machines.
2. **The first admin.** Off loopback with zero users, first boot prints the admin's username and a one-time password, as it does now. The first password sign-in must set a new password.
3. **Users and roles in the UI and the CLI.** An admin gets a Users page (add, disable, reset password, change role) and a Roles page (edit a role's permissions, using the existing rows). The CLI gets `wfx user add|ls|disable|passwd|role` and `wfx role ls|set`. The seeded roles stay `admin`, `publisher` and `viewer`. Adding a custom role is a row; nothing is hard-coded.
4. **Brute force.** Per-username and per-IP attempt limits reuse the existing rate limit and attempt counter (`auth.go`). An unknown username and a wrong password look identical. Passwords have a minimum length (12) and are never logged.
5. **Loopback stays open.** A local single-machine install still needs no login (ADR 0017). The password form appears only when auth is required.

## Consequences

- One paired migration (PG and SQLite) adds `password_hash`, `must_change` and `disabled` to users.
- A password is a new secret to protect. It is stored only as an argon2id hash and is never put in an envelope, a log or a run.
- The device grant isn't removed, so existing CLI contexts and workers keep working.
- SSO (OIDC) is still left to a header or claim mapped onto a role (`authz.go:13`). This ADR doesn't add it.
