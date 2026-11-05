# 0027 — Every workflow is a hook and an MCP tool, behind its own token

Status: accepted · 2026-11-06

## Context

wfnexus could only be started from inside: the UI, `wfx run`, a cron, a
channel. It is an MCP *client* (`cmd/wfx/skillmcp.go`, the catalog's `Mcp`
entries) but serves no MCP, and the one inbound trigger,
`POST /api/workflows/{name}/dispatches`, sits behind the user/session gate —
fine for an operator, useless for GitHub, Stripe or another agent that holds
no wfnexus account and should never hold one.

## Decision

Two public doors, both outside `/api`, both off by default, both opened per
workflow by a person:

- **Hook** — `POST /hooks/{project}/{workflow}`. The JSON body is the run's
  input (validated by `input_schema` exactly like a manual run).
  `Authorization: Bearer <token>`, or `X-WFX-Signature: sha256=<hex>` — an
  HMAC-SHA256 of the raw body keyed with the hex `sha256(token)`, for senders
  that sign rather than carry a header. `?wait=1[&timeout=s]` waits (default
  30s, max 120s) and answers the last finished step's output; otherwise 202
  with the run id. `Idempotency-Key` maps to one run per (project, workflow):
  a retry answers the first run with `Idempotent-Replayed: true`.
- **MCP** — streamable HTTP (stateless) at `/mcp/{project}/{workflow}` and
  `/mcp/{project}`. Each reachable workflow is one tool: name = short workflow
  name, input schema = `input_schema`, description = `description`.
  `tools/call` starts a run, waits like the hook, and returns the result as
  structured content — or the run id and status when it is still going.
  Built on `github.com/mark3labs/mcp-go`, already in `go.mod`.

**Tokens.** `wfxe_` + 32 random bytes. One table, `expose_tokens`: project,
workflow (`''` = project token), kind (`hook`|`mcp`), `sha256(token)`, a
12-character prefix for display, created/revoked/last-used. Plaintext exists
once — in the answer that issued it. Issue again = rotate (old one revoked in
the same call); revoke = off. A workflow token reaches that workflow and that
kind only. A project token is MCP-only and reaches the workflows that opted in
with their own MCP token — so opting in stays per workflow. Every failure
(missing, wrong, revoked, wrong workflow, wrong kind, unexposed) answers the
same 401, so the door reveals nothing. Tokens are never logged (they travel in
headers; the request log prints only the path). Runs are tagged
`triggeredBy = hook:<token id>` / `mcp:<token id>`.

**Rate limit.** An in-memory token bucket, 60/min burst 20, keyed by client
address before auth and by token id after.

**Managed** through `/api/workflows/{name}/expose[/{kind}]` (workflows
permissions) and `/api/projects/{name}/expose/mcp` (projects permissions),
the workflow page's **Expose** tab, and `wfx expose hook|mcp <wf>
[--rotate|--revoke]`, `wfx expose mcp --project P`.

## Consequences

- The HMAC key is the stored hash, so a database leak exposes signing keys
  (not bearer tokens). Accepted: the alternative is storing a reversible
  secret, and a leaked DB is already a full compromise of the run history.
- The rate limiter is per process; several API replicas each allow the rate.
- `?wait` and `tools/call` hold a connection open while they poll; a run that
  pauses for approval answers immediately with that status.
- `/dispatches` stays as is; it is the operator-side door.
