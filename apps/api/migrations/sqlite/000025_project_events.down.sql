drop table if exists event_deliveries;
alter table projects drop column events_secret_sealed;
alter table projects drop column events_url;
