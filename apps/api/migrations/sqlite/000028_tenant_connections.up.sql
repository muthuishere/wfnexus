-- See the Postgres 000034.
CREATE TABLE tenant_connections (
    id             text PRIMARY KEY,
    tenant_id      text NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name           text NOT NULL,
    kind           text NOT NULL DEFAULT 'mcp',
    endpoint       text NOT NULL,
    header_name    text NOT NULL DEFAULT 'Authorization',
    header_scheme  text NOT NULL DEFAULT 'Bearer',
    credential_enc blob,
    created_at     timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    UNIQUE (tenant_id, name)
);

CREATE TABLE tenant_connection_tools (
    connection_id text NOT NULL REFERENCES tenant_connections(id) ON DELETE CASCADE,
    tool          text NOT NULL,
    PRIMARY KEY (connection_id, tool)
);
