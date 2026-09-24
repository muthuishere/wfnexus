-- The SQLite half of the Postgres 000010 identity migration (ADR 0017).
-- Same columns, this dialect: text for uuid, timestamp for timestamptz, and a
-- JSON array for roles.permissions where Postgres has text[].
CREATE TABLE users (
    id            text PRIMARY KEY,
    name          text NOT NULL,
    display_name  text NOT NULL DEFAULT '',
    role          text NOT NULL,
    project       text NOT NULL DEFAULT '',
    disabled      boolean NOT NULL DEFAULT 0,
    created_at    timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE UNIQUE INDEX users_name_key ON users (name);

CREATE TABLE roles (
    name        text PRIMARY KEY,
    permissions text NOT NULL DEFAULT '[]'
);

CREATE TABLE user_tokens (
    id          text PRIMARY KEY,
    user_id     text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  text NOT NULL,
    label       text NOT NULL DEFAULT '',
    project     text NOT NULL DEFAULT '',
    created_at  timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    last_used   timestamp
);
CREATE UNIQUE INDEX user_tokens_hash_key ON user_tokens (token_hash);

CREATE TABLE device_codes (
    device_code text PRIMARY KEY,
    user_code   text NOT NULL,
    client_id   text NOT NULL DEFAULT '',
    host_name   text NOT NULL DEFAULT '',
    scope       text NOT NULL DEFAULT '',
    approved_by text REFERENCES users(id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'pending',
    attempts    integer NOT NULL DEFAULT 0,
    last_poll   timestamp,
    expires_at  timestamp NOT NULL,
    created_at  timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE UNIQUE INDEX device_codes_user_code_key ON device_codes (user_code);
