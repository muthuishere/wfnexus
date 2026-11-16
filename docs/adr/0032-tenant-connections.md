# 0032: A tenant's connections

Status: proposed · 2026-11-16 · amends 0030

## Context

A workflow run in a tenant needs to call tools that another service exposes over MCP, with a credential that service issued for that tenant. The server's own `mcp.json` holds the operator's servers and credentials. A tenant's run must never reach them, and one tenant's credential must never reach another tenant's run.

## Decision

1. **A connection is a row of one tenant.** It holds:
   - a name;
   - an MCP endpoint (https, or http only to loopback, with no credentials in the URL);
   - a header name and scheme;
   - the credential, sealed with the server's env-store key;
   - the tools the tenant's runs may call.

   With no key, a credential is refused. No route returns the credential, and the store's listing drops it.
2. **Only an operator manages connections** (superadmin, or loopback). A service principal or an issuer account cannot read or change them.
3. **A run in a non-default tenant resolves its agent step's `mcp:` names to that tenant's connections, and only to them.**
   - The tenant is read from the stored run row, fail-closed (the row's tenant and its project's current tenant must agree), never from the workflow file or the input.
   - A name with no connection in that tenant is an error. `mcp.json` is not consulted.
4. **The step's YAML still chooses which connections a step gets.** The connection's tool list then narrows them. Two walls, each tested on its own:
   - **the filter:** only MCP tools whose prefixed name is on the list are registered, so the agent can neither see nor call any other tool;
   - **the per-server list:** the tools are also passed to toolnexus, which narrows the listing.

   An empty list means no tools, and no connection is opened. toolnexus would read an empty list as "all", which is why the filter is the guard.
5. **The credential is opened in memory** for the toolkit's request header. It is registered with the run's redactions, so it is scrubbed from events in raw, base64 and JSON-escaped forms.
6. **No worker.** A step whose run is in a non-default tenant and that names MCP servers is refused by `packAgentJob`, because the credential would ride in the job.
7. **Off.** Tenant connections are switched off, and nothing in configuration switches them on. The default tenant always resolves names from `mcp.json`, exactly as before.

## Consequences

- One credential per connection, sent as one header. Other auth flows (OAuth client credentials, for example) are a later change.
- Connection and tool names are the service's own. The service stays the authority on what its credential may do; the list here can only narrow it.
- The run passes its id to toolkit assembly through the context (`withStepRun`). If that hand-off were lost, a tenant step with `mcp:` would fail with "no run". That is closed, not open, so it is not tested through a full run.
- Redaction of a value outside an event (a step or run error) follows the server's existing scrubbing of run secrets.
