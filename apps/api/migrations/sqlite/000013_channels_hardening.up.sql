-- See migrations/000019_channels_hardening.up.sql for why.
DROP TABLE IF EXISTS pause_codes;
CREATE TABLE pause_codes (
    channel     text NOT NULL,
    code        text NOT NULL,
    run_id      text NOT NULL,
    step_id     text NOT NULL,
    kind        text NOT NULL DEFAULT '',
    created_at  timestamp NOT NULL,
    expires_at  timestamp NOT NULL,
    retired_at  timestamp,
    PRIMARY KEY (channel, code)
);
CREATE INDEX IF NOT EXISTS pause_codes_step_idx ON pause_codes (run_id, step_id);

ALTER TABLE channel_messages ADD COLUMN handled boolean NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS channel_messages_pending_idx ON channel_messages (channel, handled);
