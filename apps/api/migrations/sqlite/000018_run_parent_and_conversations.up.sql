-- The SQLite half of the Postgres 000022 migration (ADR 0026 decisions 4 and 5).
-- See migrations/000022_run_parent_and_conversations.up.sql for the columns.
ALTER TABLE workflow_runs ADD COLUMN parent_run_id text NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN parent_step_id text NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN parent_mode text NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN conversation text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS workflow_runs_parent_idx ON workflow_runs (parent_run_id);

ALTER TABLE workflow_memory ADD COLUMN conversation text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS conversations (
    channel     text    NOT NULL,
    chat        text    NOT NULL,
    workflow    text    NOT NULL DEFAULT '',
    project     text    NOT NULL DEFAULT '',
    state       text    NOT NULL DEFAULT 'active',
    last_run_id text    NOT NULL DEFAULT '',
    created_ns  integer NOT NULL,
    updated_ns  integer NOT NULL,
    PRIMARY KEY (channel, chat)
);
