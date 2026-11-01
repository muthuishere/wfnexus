-- WORKFLOW MEMORY: what previous runs of a workflow learned (engine/memory.go).
--
-- One row per thing remembered, keyed by (project, workflow):
--
--   kind = 'run'     the deterministic record a finished run leaves (run_id set)
--   kind = 'note'    a lesson an agent wrote with the `remember` tool
--   kind = 'digest'  the rolling summary older run records fold into (one per workflow)
--
-- created_ns orders newest-first without depending on timestamp precision,
-- which differs between Postgres and SQLite. Bodies are scrubbed of every
-- redacted value before they are written; this is not a secret store.
create table if not exists workflow_memory (
    id         text   primary key,
    project    text   not null default '',
    workflow   text   not null,
    run_id     text   not null default '',
    kind       text   not null,
    status     text   not null default '',
    step_id    text   not null default '',
    body       text   not null,
    created_ns bigint not null
);
create index if not exists workflow_memory_by_workflow
    on workflow_memory (workflow, project, kind, created_ns);
