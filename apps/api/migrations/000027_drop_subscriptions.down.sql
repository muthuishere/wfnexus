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
