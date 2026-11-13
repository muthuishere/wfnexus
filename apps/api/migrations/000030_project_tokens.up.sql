-- PROJECT SERVICE TOKENS: a machine credential confined to ONE project — what
-- a platform that provisions projects (create, mint, revoke, delete) hands to
-- its own backend. Only sha256(token) is stored; the plaintext is shown once.
-- revoked_ns = 0 means active.
create table if not exists project_tokens (
    id           text   primary key,
    project      text   not null,
    label        text   not null default '',
    token_hash   text   not null unique,
    prefix       text   not null default '',
    created_ns   bigint not null,
    revoked_ns   bigint not null default 0,
    last_used_ns bigint not null default 0
);
create index if not exists project_tokens_by_project on project_tokens (project, revoked_ns);
