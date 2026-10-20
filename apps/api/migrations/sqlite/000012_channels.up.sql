-- See migrations/000018_channels.up.sql for what these tables are.
CREATE TABLE IF NOT EXISTS channel_messages (
    id          integer PRIMARY KEY AUTOINCREMENT,
    channel     text NOT NULL,
    msg_id      text NOT NULL,
    sender      text NOT NULL DEFAULT '',
    sender_name text NOT NULL DEFAULT '',
    text        text NOT NULL DEFAULT '',
    attachments text NOT NULL DEFAULT '[]',
    sent_at     timestamp,
    run_id      text NOT NULL DEFAULT '',
    outcome     text NOT NULL DEFAULT '',
    received_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    UNIQUE (channel, msg_id)
);
CREATE INDEX IF NOT EXISTS channel_messages_run_idx ON channel_messages (run_id);

CREATE TABLE IF NOT EXISTS channel_dedup (
    channel   text NOT NULL,
    dedup_key text NOT NULL,
    sent_at   timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (channel, dedup_key)
);

CREATE TABLE IF NOT EXISTS pause_codes (
    code       text PRIMARY KEY,
    run_id     text NOT NULL,
    step_id    text NOT NULL,
    kind       text NOT NULL DEFAULT '',
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
