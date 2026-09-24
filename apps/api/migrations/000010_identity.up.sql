-- Identity: who a request is from (ADR 0017).
--
-- Users and workers are SEPARATE tables on purpose. Revoking every user token
-- must not disturb a machine mid-job, and a worker has labels where a user has
-- a role and a project scope. What they share is the mint/hash/compare path
-- (internal/auth) and the `Authorization: Bearer` wire format — ADR 0017's
-- "one path" read as one CODE path, not one table.
create table if not exists users (
    id            uuid primary key,
    name          text        not null unique,
    display_name  text        not null default '',
    role          text        not null,            -- names a row in roles
    project       text        not null default '', -- '' = every project the role allows
    disabled      boolean     not null default false,
    created_at    timestamptz not null default now()
);

-- Roles are ROWS, editable, never a set baked into the binary (ADR 0017).
create table if not exists roles (
    name        text primary key,
    permissions text[] not null default '{}'
);

create table if not exists user_tokens (
    id          uuid primary key,
    user_id     uuid not null references users(id) on delete cascade,
    token_hash  text not null unique,   -- HashToken only. The value is never stored.
    label       text not null default '',
    project     text not null default '',
    created_at  timestamptz not null default now(),
    last_used   timestamptz
);

-- RFC 8628 §3.1-3.5. The device_code is hashed by the same rule as a token:
-- it is a bearer credential for the length of the flow, and a row that held it
-- in the clear would be a credential at rest.
create table if not exists device_codes (
    device_code text primary key,
    user_code   text        not null unique,
    client_id   text        not null default '',
    host_name   text        not null default '',
    scope       text        not null default '',
    approved_by uuid        references users(id) on delete cascade,
    status      text        not null default 'pending',  -- pending|approved|denied
    attempts    int         not null default 0,
    last_poll   timestamptz,
    expires_at  timestamptz not null,
    created_at  timestamptz not null default now()
);
