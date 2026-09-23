-- Environment the platform holds, rather than the workflow file.
--
-- Two scopes above the file: SYSTEM (every run on this platform) and PROJECT
-- (every run of one repository's workflows). A workflow's own `env:` layers on
-- top, so the order is system → project → workflow → job → step, each level
-- only overriding the names it mentions.
--
-- Values are ENCRYPTED. The threat is the ordinary one — a pg_dump, a backup on
-- a laptop, a replica, a screenshot — and none of those should hand over a
-- token. The key is not in this database.
create table if not exists env_vars (
    scope      text        not null,          -- 'system' | 'project'
    scope_name text        not null default '', -- the project, '' for system
    key        text        not null,
    value_enc  bytea       not null,
    -- A secret is never shown again, not in the API, the UI or the CLI. A
    -- non-secret is ordinary configuration — a URL, a flag — and hiding it
    -- would only make the store useless for the thing it is mostly used for.
    secret     boolean     not null default true,
    updated_at timestamptz not null default now(),
    primary key (scope, scope_name, key)
);
