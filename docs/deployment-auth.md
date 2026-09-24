# Deployment note — where authentication comes from (ADR 0017)

The server decides whether authentication applies from **its own bind address**,
once, at construction. There is no setting that changes it, and the absence of
that setting is itself the requirement.

| bind | users | behaviour |
|---|---|---|
| `127.0.0.1:8090` (the default) | any | **auth ABSENT.** No identity on the request, no anonymous user row, no default password. |
| anything else | zero | **REFUSES TO SERVE.** Prints a one-time admin credential and exits; start it again and it serves. |
| anything else | one or more | **auth REQUIRED.** Every `/api` request resolves a user or a worker subject, or is refused. |

One line in the boot log states which of the three it is (`auth: ABSENT …` /
`auth: REQUIRED …`), and the refusal is printed to stdout, not to the log.

The default bind is already `127.0.0.1:8090`
(`apps/api/internal/config/config.go:190`). The container and k8s manifests
state `WFX_ADDR=:8090` themselves — a deployment listens widely because it says
so, and it must therefore have an administrator.

## Two honest gaps

1. **A reverse proxy in front of a loopback bind re-exposes an unauthenticated
   API.** No bind-address rule can see that tunnel; it is the operator's own.
   If you put a proxy in front of wfx, bind wfx off loopback and let it require
   a subject — do not proxy to a loopback bind and rely on the proxy for auth.
2. **`127.0.0.1` is shared by every local user and every process on the box.**
   "Loopback" therefore means "this machine is the trust boundary". That is the
   single-machine premise, stated rather than assumed.

## Logging in

`wfx login --url <host>` uses the OAuth 2.0 Device Authorization Grant
(RFC 8628): it prints a code and polls, binds no port and opens no browser, so
it works over SSH, inside a container and inside another agent's session. The
human approves at `<host>/device`. Credentials live in
`~/.config/wfx/contexts.json`, mode `0600`; a group- or world-readable one is
refused, not warned about. `wfx logout` revokes at the host and removes the
local entry — and removes it even when the host is unreachable.
