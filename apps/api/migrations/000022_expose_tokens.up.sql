-- EXPOSURE TOKENS (ADR 0027): who may start a workflow from OUTSIDE — an
-- inbound hook (POST /hooks/{project}/{workflow}) or an MCP client
-- (/mcp/{project}[/{workflow}]).
--
-- One row per token ever issued. workflow = '' is a PROJECT token (MCP only):
-- it reaches every workflow of the project that has its own active mcp token.
-- Only sha256(token) is stored; the plaintext is shown once and never again.
-- revoked_ns = 0 means active. A workflow with no active row is not exposed.
create table if not exists expose_tokens (
    id           text   primary key,
    project      text   not null,
    workflow     text   not null default '',
    kind         text   not null,
    token_hash   text   not null unique,
    prefix       text   not null default '',
    created_ns   bigint not null,
    revoked_ns   bigint not null default 0,
    last_used_ns bigint not null default 0
);
create index if not exists expose_tokens_by_target on expose_tokens (project, workflow, kind, revoked_ns);

-- Idempotency-Key → the run it started, per (project, workflow).
create table if not exists hook_idempotency (
    project    text   not null,
    workflow   text   not null,
    key        text   not null,
    run_id     text   not null default '',
    created_ns bigint not null,
    primary key (project, workflow, key)
);
