-- A WORKER is a machine that has joined the pool, exactly as a GitHub
-- self-hosted runner or a Jenkins node does: it registers with LABELS, and a
-- step's `runs-on` names a label, never a machine. The platform can live in
-- Kubernetes and still place a step on somebody's Windows box.
create table if not exists workers (
    id          uuid primary key,
    name        text        not null,
    labels      text[]      not null default '{}',
    os          text        not null default '',
    arch        text        not null default '',
    version     text        not null default '',
    token_hash  text        not null,
    last_seen   timestamptz not null default now(),
    created_at  timestamptz not null default now()
);
create index if not exists workers_labels_idx on workers using gin (labels);

-- A JOB is one step's work, queued for whoever holds its label. It is a row
-- rather than a channel so the queue survives a restart of either side and a
-- worker that dies mid-job leaves evidence instead of a silence.
create table if not exists worker_jobs (
    id          uuid primary key,
    run_id      uuid        not null references workflow_runs(id) on delete cascade,
    step_id     text        not null,
    label       text        not null,
    worker_id   uuid        references workers(id) on delete set null,
    status      text        not null default 'queued',  -- queued | leased | done
    payload     jsonb       not null,
    result      jsonb,
    created_at  timestamptz not null default now(),
    leased_at   timestamptz,
    finished_at timestamptz
);
create index if not exists worker_jobs_queue_idx on worker_jobs (status, label, created_at);

-- One row table for things the platform generates once and must not lose — the
-- registration token being the first of them.
create table if not exists settings (
    key   text primary key,
    value text not null
);
