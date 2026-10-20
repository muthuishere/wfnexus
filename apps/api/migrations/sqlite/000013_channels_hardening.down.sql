DROP INDEX IF EXISTS channel_messages_pending_idx;
ALTER TABLE channel_messages DROP COLUMN handled;
DROP TABLE IF EXISTS pause_codes;
CREATE TABLE pause_codes (
    code       text PRIMARY KEY,
    run_id     text NOT NULL,
    step_id    text NOT NULL,
    kind       text NOT NULL DEFAULT '',
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
