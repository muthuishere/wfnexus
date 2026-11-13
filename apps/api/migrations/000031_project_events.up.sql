-- RUN EVENTS OUT (plan 18, W3): a project names one URL; every run of the
-- project posts started / waiting_for_approval / finished / failed to it,
-- HMAC-signed with an event id, at least once.
alter table projects add column events_url text not null default '';
-- The signing secret, sealed with the server's env-store key. Shown once.
alter table projects add column events_secret_sealed bytea;

-- The outbox: one row per event, written when the run changes status and
-- delivered by a retrying loop. event_id is deterministic per run and
-- transition, so writing it twice is a no-op and a receiver can dedupe on it.
create table if not exists event_deliveries (
    event_id     text    primary key,
    project      text    not null,
    type         text    not null,
    body         text    not null,
    attempts     integer not null default 0,
    next_ns      bigint  not null,
    delivered_ns bigint  not null default 0,
    last_error   text    not null default '',
    created_ns   bigint  not null
);
create index if not exists event_deliveries_due on event_deliveries (delivered_ns, next_ns);
