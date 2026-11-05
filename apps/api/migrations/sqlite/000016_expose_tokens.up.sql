-- See migrations/000022_expose_tokens.up.sql for what these tables are.
create table if not exists expose_tokens (
    id           text   primary key,
    project      text   not null,
    workflow     text   not null default '',
    kind         text   not null,
    token_hash   text   not null unique,
    prefix       text   not null default '',
    created_ns   integer not null,
    revoked_ns   integer not null default 0,
    last_used_ns integer not null default 0
);
create index if not exists expose_tokens_by_target on expose_tokens (project, workflow, kind, revoked_ns);

create table if not exists hook_idempotency (
    project    text   not null,
    workflow   text   not null,
    key        text   not null,
    run_id     text   not null default '',
    created_ns integer not null,
    primary key (project, workflow, key)
);
