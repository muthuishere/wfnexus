-- The in-engine subscriptions router is gone (#104, ADR 0028): a service
-- module + workflow hooks replace it. Its delivery ledger goes with it.
-- channel_messages.event stays: it is the received message's own fields.
drop table if exists subscription_deliveries;
