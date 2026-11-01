-- See migrations/000021_workflow_memory.up.sql for what this table is.
CREATE TABLE IF NOT EXISTS workflow_memory (
    id         text    PRIMARY KEY,
    project    text    NOT NULL DEFAULT '',
    workflow   text    NOT NULL,
    run_id     text    NOT NULL DEFAULT '',
    kind       text    NOT NULL,
    status     text    NOT NULL DEFAULT '',
    step_id    text    NOT NULL DEFAULT '',
    body       text    NOT NULL,
    created_ns integer NOT NULL
);
CREATE INDEX IF NOT EXISTS workflow_memory_by_workflow
    ON workflow_memory (workflow, project, kind, created_ns);
