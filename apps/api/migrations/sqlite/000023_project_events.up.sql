-- RUN EVENTS OUT: a project names one URL; every run of the project posts
-- started / waiting_for_approval / finished / failed to it, HMAC-signed, at
-- least once.
create table if not exists project_events (
    project       text primary key,
    url           text not null,
    -- The signing secret, sealed with the server's env-store key. Shown once.
    secret_sealed blob
);

-- The outbox: one row per event, written when the run changes status and
-- delivered by a retrying loop. event_id = <stem>.<seq>: the stem names the
-- run and transition, seq counts how often that transition happened (a run
-- that is retried starts, fails or waits again). The id is stable across
-- delivery attempts, so a receiver dedupes on it.
create table if not exists event_deliveries (
    event_id     text    primary key,
    stem         text    not null,
    seq          integer not null,
    run_id       text    not null,
    project      text    not null,
    type         text    not null,
    body         text    not null,
    attempts     integer not null default 0,
    next_ns      bigint  not null,
    delivered_ns bigint  not null default 0,
    last_error   text    not null default '',
    created_ns   bigint  not null,
    unique (stem, seq)
);
create index if not exists event_deliveries_due on event_deliveries (delivered_ns, next_ns);
create index if not exists event_deliveries_run on event_deliveries (run_id, created_ns);
