CREATE TABLE workflow_runs (
    id           uuid PRIMARY KEY,
    workflow     text NOT NULL,
    status       text NOT NULL,            -- queued|running|awaiting_approval|needs_input|done|failed|cancelled
    input        jsonb NOT NULL DEFAULT '{}'::jsonb,
    current_step text NOT NULL DEFAULT '',
    error        text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workflow_runs_created_idx ON workflow_runs (created_at DESC);

CREATE TABLE step_runs (
    id          uuid PRIMARY KEY,
    run_id      uuid NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_id     text NOT NULL,
    position    int  NOT NULL,
    status      text NOT NULL,             -- pending|running|done|failed|awaiting_approval|approved|rejected|needs_input|skipped
    attempts    int  NOT NULL DEFAULT 0,
    turns       int  NOT NULL DEFAULT 0,
    prompt      text NOT NULL DEFAULT '',
    output      jsonb,
    raw_text    text NOT NULL DEFAULT '',
    error       text NOT NULL DEFAULT '',
    usage       jsonb NOT NULL DEFAULT '{}'::jsonb,
    started_at  timestamptz,
    finished_at timestamptz,
    UNIQUE (run_id, step_id)
);

CREATE TABLE run_events (
    id         bigserial PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_id    text NOT NULL DEFAULT '',
    kind       text NOT NULL,              -- run.status|step.status|llm|tool_call|tool_result|text|log|error
    payload    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX run_events_run_idx ON run_events (run_id, id);

CREATE TABLE artifacts (
    id           uuid PRIMARY KEY,
    run_id       uuid NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_id      text NOT NULL DEFAULT '',
    name         text NOT NULL,
    object_key   text NOT NULL,
    content_type text NOT NULL DEFAULT 'application/octet-stream',
    size_bytes   bigint NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX artifacts_run_idx ON artifacts (run_id, created_at);
