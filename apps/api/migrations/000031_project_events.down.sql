drop table if exists event_deliveries;
alter table projects drop column if exists events_secret_sealed;
alter table projects drop column if exists events_url;
