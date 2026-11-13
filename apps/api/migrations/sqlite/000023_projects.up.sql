-- The PROJECTS registry: one row per project (tenant) this server knows.
-- The workflow files stay on disk per project; this row is what a hosted
-- server hangs a project's metadata on (tokens, event URL, quotas, usage).
create table if not exists projects (
    name       text   primary key,
    created_ns bigint not null
);
