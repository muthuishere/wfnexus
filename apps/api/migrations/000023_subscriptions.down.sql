drop table if exists subscription_deliveries;
alter table channel_messages drop column if exists event;
