-- The SQLite schema, for `mode: local`.
--
-- This is a SEPARATE migration lineage from the Postgres files one directory
-- up, not a translation applied to them. Those are already applied in real
-- databases and must never be edited, and they are written in types SQLite does
-- not have: uuid, jsonb, text[], bytea, bigserial, GIN indexes. Keeping two
-- lineages is honest about that; it also means the SQLite file gets the schema
-- as it stands today rather than replaying four years of ALTERs.
--
-- The rule from AGENTS.md holds here too: once this has shipped, changes come
-- as a NEW 0000NN file in this directory, alongside the Postgres one.
--
-- Types are chosen so the shared queries do not have to know which engine they
-- are on: a uuid is text (google/uuid scans a string), jsonb is text (every
-- JSON column is read through rawJSON), bytea is blob, and a timestamp is text
-- in a format the driver returns as time.Time. Defaults carry sub-second
-- precision because runs are listed newest-first and two runs created in the
-- same second must still order.

CREATE TABLE workflow_runs (
    id           text PRIMARY KEY,
    project      text NOT NULL DEFAULT 'local',
    workflow     text NOT NULL,
    status       text NOT NULL,
    input        text NOT NULL DEFAULT '{}',
    current_step text NOT NULL DEFAULT '',
    base_ref     text NOT NULL DEFAULT '',
    error        text NOT NULL DEFAULT '',
    created_at   timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    updated_at   timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE INDEX workflow_runs_created_idx ON workflow_runs (created_at DESC);
CREATE INDEX workflow_runs_project_created_idx ON workflow_runs (project, created_at DESC);
CREATE INDEX workflow_runs_project_workflow_idx ON workflow_runs (project, workflow, created_at DESC);

CREATE TABLE step_runs (
    id          text PRIMARY KEY,
    run_id      text NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_id     text NOT NULL,
    position    integer NOT NULL,
    status      text NOT NULL,
    attempts    integer NOT NULL DEFAULT 0,
    turns       integer NOT NULL DEFAULT 0,
    prompt      text NOT NULL DEFAULT '',
    output      text,
    raw_text    text NOT NULL DEFAULT '',
    error       text NOT NULL DEFAULT '',
    usage       text NOT NULL DEFAULT '{}',
    pending     text,
    decision    text,
    started_at  timestamp,
    finished_at timestamp,
    UNIQUE (run_id, step_id)
);

CREATE TABLE run_events (
    id         integer PRIMARY KEY AUTOINCREMENT,
    run_id     text NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_id    text NOT NULL DEFAULT '',
    kind       text NOT NULL,
    payload    text NOT NULL DEFAULT '{}',
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE INDEX run_events_run_idx ON run_events (run_id, id);

CREATE TABLE artifacts (
    id           text PRIMARY KEY,
    run_id       text NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_id      text NOT NULL DEFAULT '',
    name         text NOT NULL,
    object_key   text NOT NULL,
    content_type text NOT NULL DEFAULT 'application/octet-stream',
    size_bytes   integer NOT NULL DEFAULT 0,
    created_at   timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE INDEX artifacts_run_idx ON artifacts (run_id, created_at);

-- labels is a JSON array rather than an array type, read and written through
-- the dialect's labels helpers; json_each() is what makes it queryable.
CREATE TABLE workers (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    labels      text NOT NULL DEFAULT '[]',
    os          text NOT NULL DEFAULT '',
    arch        text NOT NULL DEFAULT '',
    version     text NOT NULL DEFAULT '',
    token_hash  text NOT NULL,
    last_seen   timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    created_at  timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE UNIQUE INDEX workers_name_key ON workers (name);

CREATE TABLE worker_jobs (
    id          text PRIMARY KEY,
    run_id      text NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_id     text NOT NULL,
    label       text NOT NULL,
    worker_id   text REFERENCES workers(id) ON DELETE SET NULL,
    status      text NOT NULL DEFAULT 'queued',
    payload     text NOT NULL,
    result      text,
    created_at  timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    leased_at   timestamp,
    finished_at timestamp
);
CREATE INDEX worker_jobs_queue_idx ON worker_jobs (status, label, created_at);

CREATE TABLE settings (
    key   text PRIMARY KEY,
    value text NOT NULL
);

CREATE TABLE env_vars (
    scope      text NOT NULL,
    scope_name text NOT NULL DEFAULT '',
    key        text NOT NULL,
    value_enc  blob NOT NULL,
    secret     boolean NOT NULL DEFAULT 1,
    updated_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (scope, scope_name, key)
);
