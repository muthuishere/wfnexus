-- ADR 0026 decisions 4 and 5: the `start:` step and conversations.
--
-- parent_run_id   the run whose `start:` step started this one ('' for none).
--                 The child is an independent run; the link is what lets the
--                 run page show "started by" and the parent's children.
-- parent_step_id  which step of the parent started it.
-- parent_mode     how the parent waits: wait | nowait | event.
-- conversation    the conversation key (channel + chat) a run belongs to, ''
--                 for a run that did not come from a chat. Memory is keyed by
--                 it, and runs of one conversation go one at a time.
ALTER TABLE workflow_runs ADD COLUMN parent_run_id text NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN parent_step_id text NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN parent_mode text NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN conversation text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS workflow_runs_parent_idx ON workflow_runs (parent_run_id);

ALTER TABLE workflow_memory ADD COLUMN conversation text NOT NULL DEFAULT '';

-- One row per conversation, keyed (channel, chat). state is
-- active | waiting | resolved | closed; a new message reopens it.
CREATE TABLE IF NOT EXISTS conversations (
    channel     text   NOT NULL,
    chat        text   NOT NULL,
    workflow    text   NOT NULL DEFAULT '',
    project     text   NOT NULL DEFAULT '',
    state       text   NOT NULL DEFAULT 'active',
    last_run_id text   NOT NULL DEFAULT '',
    created_ns  bigint NOT NULL,
    updated_ns  bigint NOT NULL,
    PRIMARY KEY (channel, chat)
);
