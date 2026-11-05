-- Subscriptions and the router (ADR 0026).
--
-- channel_messages.event    the ADR 0026 event fields (chat, chatType, kind,
--                           replyTo, media, mentions, fromOwner, reaction), so a
--                           message re-handled after a crash routes exactly as
--                           it would have the first time.
-- subscription_deliveries   one row per (subscription, message id): a message
--                           seen twice starts no second run. `outcome` says
--                           what the router did with it.
alter table channel_messages add column if not exists event jsonb not null default '{}';

create table if not exists subscription_deliveries (
    subscription text        not null,
    msg_id       text        not null,
    channel      text        not null default '',
    chat         text        not null default '',
    run_id       text        not null default '',
    outcome      text        not null default '',
    created_at   timestamptz not null,
    primary key (subscription, msg_id)
);
create index if not exists subscription_deliveries_created_idx on subscription_deliveries (subscription, created_at);
