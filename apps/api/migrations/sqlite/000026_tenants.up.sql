-- TENANTS (see the Postgres 000032 for the rules). SQLite has no row-level
-- security either; isolation is enforced by the application.
CREATE TABLE tenants (
    id         text PRIMARY KEY,
    name       text NOT NULL DEFAULT '',
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
INSERT INTO tenants (id, name) VALUES ('default', 'default');

CREATE TABLE tenant_projects (
    project    text PRIMARY KEY,
    tenant_id  text NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);

CREATE TABLE service_principals (
    id          text PRIMARY KEY,
    tenant_id   text NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        text NOT NULL,
    token_hash  text NOT NULL UNIQUE,
    prefix      text NOT NULL DEFAULT '',
    endpoint    text NOT NULL DEFAULT '',
    created_at  timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    revoked_at  timestamp,
    last_used   timestamp,
    UNIQUE (tenant_id, name)
);

CREATE TABLE service_principal_tools (
    principal_id text NOT NULL REFERENCES service_principals(id) ON DELETE CASCADE,
    tool         text NOT NULL,
    PRIMARY KEY (principal_id, tool)
);

-- No REFERENCES: SQLite refuses a foreign key with a non-NULL default on an
-- added column. The value is only ever written from tenant_projects.
ALTER TABLE workflow_runs ADD COLUMN tenant_id text NOT NULL DEFAULT 'default';
CREATE INDEX workflow_runs_tenant_created_idx ON workflow_runs (tenant_id, created_at DESC);
ALTER TABLE published_bundles ADD COLUMN tenant_id text NOT NULL DEFAULT 'default';
