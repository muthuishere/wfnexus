-- A tenant's CONNECTIONS: the MCP endpoints its runs may call, each with a
-- credential the endpoint's owner issued and the tools this tenant may use.
-- The credential is SEALED with the server's env-store key; there is no
-- column that holds it in the clear and no route that returns it.
create table if not exists tenant_connections (
    id             uuid primary key,
    tenant_id      text not null references tenants(id) on delete cascade,
    name           text not null,
    kind           text not null default 'mcp',
    endpoint       text not null,
    header_name    text not null default 'Authorization',
    header_scheme  text not null default 'Bearer',
    credential_enc bytea,
    created_at     timestamptz not null default now(),
    unique (tenant_id, name)
);

-- The tools of a connection a run may call. None listed = none.
create table if not exists tenant_connection_tools (
    connection_id uuid not null references tenant_connections(id) on delete cascade,
    tool          text not null,
    primary key (connection_id, tool)
);
