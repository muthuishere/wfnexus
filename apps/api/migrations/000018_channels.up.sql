-- Channels (ADR 0023): what the server sent and received through its adapters.
--
-- channel_messages  every RECEIVED message, once: (channel, msg_id) is unique,
--                   so an adapter that delivers the same message twice is a
--                   no-op. `outcome` says what it did — resolved a pause,
--                   started a run, or nothing — and `run_id` which run.
-- channel_dedup     the last send under a dedup key, so a 15-minute schedule
--                   saying the same thing does not say it every 15 minutes.
-- pause_codes       the short code an outbound pause carries, mapped back to
--                   the paused step. The code is a LOOKUP, not a credential:
--                   a reply resolves only from an allow-listed sender.
--
-- The receive cursor is a row in `settings` (channel.cursor.<name>).
create table if not exists channel_messages (
    id          bigserial   primary key,
    channel     text        not null,
    msg_id      text        not null,
    sender      text        not null default '',
    sender_name text        not null default '',
    text        text        not null default '',
    attachments jsonb       not null default '[]',
    sent_at     timestamptz,
    run_id      text        not null default '',
    outcome     text        not null default '',
    received_at timestamptz not null default now(),
    unique (channel, msg_id)
);
create index if not exists channel_messages_run_idx on channel_messages (run_id);

create table if not exists channel_dedup (
    channel   text        not null,
    dedup_key text        not null,
    sent_at   timestamptz not null default now(),
    primary key (channel, dedup_key)
);

create table if not exists pause_codes (
    code       text        primary key,
    run_id     text        not null,
    step_id    text        not null,
    kind       text        not null default '',
    created_at timestamptz not null default now()
);
