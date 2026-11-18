-- See the Postgres migration 000031.
ALTER TABLE users ADD COLUMN auth_claim text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS link_rejections (
    user_id    text      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider   text      NOT NULL,
    subject    text      NOT NULL,
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (user_id, provider, subject)
);
-- Case-insensitive lookups at sign-in use these rather than scanning users.
CREATE INDEX IF NOT EXISTS users_lower_name_idx ON users (lower(name));
CREATE INDEX IF NOT EXISTS users_auth_subject_lower_idx ON users (auth_provider, lower(auth_subject));
