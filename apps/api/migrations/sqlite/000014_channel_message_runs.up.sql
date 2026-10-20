-- See migrations/000020_channel_message_runs.up.sql for why.
CREATE TABLE IF NOT EXISTS channel_message_runs (
    channel    text NOT NULL,
    msg_id     text NOT NULL,
    workflow   text NOT NULL,
    run_id     text NOT NULL DEFAULT '',
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (channel, msg_id, workflow)
);
