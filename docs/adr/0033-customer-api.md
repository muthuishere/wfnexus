# 0033: The customer API

Status: proposed · 2026-11-17 · amends 0030, 0031, 0032

## Context

An account signed in at the external issuer (ADR 0031) needs to list, read, validate, save and run workflows of its own tenant, without an operator in between. Approval gates for write steps are not yet guaranteed by the engine (#146, #148), so nothing a customer saves may act without one.

## Decision

1. **Off.** Every customer route answers 404 and does nothing, and nothing in configuration switches it on. It is switched on only after write steps always pause for approval and the engine hands over write inputs (#146, #148).
2. **Routes under `/api/svc/`:**
   - `GET /workflows` lists;
   - `GET /workflows/{name}` reads;
   - `POST /workflows/validate` validates;
   - `POST /workflows` saves `{project, definition}`;
   - `POST /workflows/{name}/runs` starts a run (shared with service principals, ADR 0030).
3. **Only an issuer account.** The tenant comes from its token, through the operator's link. A service principal, a person signed in here and an anonymous loopback caller get 404. Another tenant's workflow, and an absent one, give the same answer.
4. **A save writes only into a project bound to the account's tenant** that is a plain directory. An unbound project belongs to the default tenant, never a customer, and is refused. A git-backed project is refused, so a save never becomes a branch or a pull request on a remote.
5. **A customer workflow cannot act.** Allowed: agent steps with no tools (prompt, model, output schema, budget, control flow, manual dispatch). Refused:
   - on a step: `run`/`shell`, `uses`/`module`, `start`, `send`, `tools`, `skills`, `team`, `env`, `runs_on`, `state`, `memory`, `mcp`, also inside loop bodies;
   - on the definition: `mount`, `files`, `jobs`, `uses`, `env`, `runs_on`, and any trigger other than a manual dispatch (schedule, hook, message, workflow_call).
6. **Connections stay operator-only** (ADR 0032). A customer cannot list, create or change them, or set any URL.

## Consequences

- Customer workflows name no MCP server in this change. When this was written, the loader checked `mcp:` names against the server's registry. A workflow naming a tenant connection was therefore skipped at load (on its own, not the whole reload), and so was an operator's workflow in a tenant project. ADR 0034 (#162) resolves a bound project's names against its tenant's connections. Customer workflows still name none until approval gates are guaranteed.
- Tenant members (people with a role in a tenant on this server) are not callers yet; only issuer accounts are.
- A save into a git-backed project (as a reviewed proposal) is a later change.
- The definition-level `uses:` and remote-pin refusal has no test of its own (an unresolved `uses:` also fails validation); every other refusal above is tested on its own.
