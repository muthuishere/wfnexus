-- See the Postgres migration 000031.
ALTER TABLE users ADD COLUMN auth_claim text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS link_rejections (
    user_id    text      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider   text      NOT NULL,
    subject    text      NOT NULL,
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (user_id, provider, subject)
);
