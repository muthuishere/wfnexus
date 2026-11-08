CREATE TABLE IF NOT EXISTS subscription_deliveries (
    subscription text NOT NULL,
    msg_id       text NOT NULL,
    channel      text NOT NULL DEFAULT '',
    chat         text NOT NULL DEFAULT '',
    run_id       text NOT NULL DEFAULT '',
    outcome      text NOT NULL DEFAULT '',
    created_at   timestamp NOT NULL,
    PRIMARY KEY (subscription, msg_id)
);
CREATE INDEX IF NOT EXISTS subscription_deliveries_created_idx ON subscription_deliveries (subscription, created_at);
