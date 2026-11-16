-- TENANTS: a boundary that runs, published bundles and service principals
-- belong to. An install with one owner has exactly one tenant, 'default', and
-- behaves as before; every existing row is backfilled to it.
--
-- Isolation is enforced by the application (the API derives the tenant from
-- the credential and filters on it). There is no row-level security here.
create table if not exists tenants (
    id         text primary key check (id ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name       text        not null default '',
    created_at timestamptz not null default now()
);
insert into tenants (id, name) values ('default', 'default') on conflict (id) do nothing;

-- A project belongs to at most one tenant. A project with no row belongs to
-- 'default'. The tenant of a run or bundle is written from this at insert.
create table if not exists tenant_projects (
    project    text primary key,
    tenant_id  text not null references tenants(id) on delete restrict,
    created_at timestamptz not null default now()
);

-- A SERVICE PRINCIPAL is a machine caller bound to exactly one tenant. Only
-- sha256(token) is stored; the plaintext is returned once, at issue.
create table if not exists service_principals (
    id          uuid primary key,
    tenant_id   text not null references tenants(id) on delete cascade,
    name        text not null,
    token_hash  text not null unique,
    prefix      text not null default '',
    -- Declared surface, a REFERENCE only: where the caller's tools live. It is
    -- not a tool registry and nothing is called through it here.
    endpoint    text not null default '',
    created_at  timestamptz not null default now(),
    revoked_at  timestamptz,
    last_used   timestamptz,
    unique (tenant_id, name)
);

create table if not exists service_principal_tools (
    principal_id uuid not null references service_principals(id) on delete cascade,
    tool         text not null,
    primary key (principal_id, tool)
);

-- The workflows a principal may START, by name. None listed = none. A name
-- is honoured only while that workflow's project is in the principal's tenant.
create table if not exists service_principal_workflows (
    principal_id uuid not null references service_principals(id) on delete cascade,
    workflow     text not null,
    primary key (principal_id, workflow)
);

alter table workflow_runs add column if not exists tenant_id text not null default 'default' references tenants(id);
create index if not exists workflow_runs_tenant_created_idx on workflow_runs (tenant_id, created_at desc);
alter table published_bundles add column if not exists tenant_id text not null default 'default' references tenants(id);
