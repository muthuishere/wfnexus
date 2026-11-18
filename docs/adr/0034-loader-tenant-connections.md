# 0034: The loader resolves a tenant project's MCP names to its connections

Status: proposed · 2026-11-17 · issue #162 · amends 0032

## Context

The loader and a save check every step's `mcp:` names against the server's own registry (`mcp.json`). A workflow in a project bound to a tenant that names one of that tenant's connections (ADR 0032) was therefore skipped at load, so connections could not be used from any workflow.

## Decision

1. **A catalog may resolve MCP names per project** (`workflow.ProjectMcp`). The loader checks each source's workflows with the catalog scoped to that source (`workflow.ForProject`). A save checks as a workflow of the project it lands in, so it accepts exactly what the next load accepts.
2. **The engine's catalog resolves by the project's tenant:**
   - default tenant: the server's registry, as before;
   - a non-default tenant: that tenant's connections only, never the registry;
   - a non-default tenant while connections are switched off: no name resolves, so the workflow is skipped with its reason;
   - a project whose tenant cannot be read: no name resolves.
3. **Run time is unchanged** (ADR 0032). The tenant is read from the run row, and the allow-list and the off-refusal apply.

## Consequences

- A workflow skipped at load costs only itself and is reported as a skip, as before.
- An operator's workflow in a tenant project can now name the tenant's connections. Customer workflows (ADR 0033) still name none.
