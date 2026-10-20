drop index if exists channel_messages_pending_idx;
alter table channel_messages drop column if exists handled;
drop table if exists pause_codes;
create table pause_codes (
    code       text        primary key,
    run_id     text        not null,
    step_id    text        not null,
    kind       text        not null default '',
    created_at timestamptz not null default now()
);
