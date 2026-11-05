-- See migrations/000022_user_passwords.up.sql (ADR 0025).
ALTER TABLE users ADD COLUMN password_hash text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN must_change boolean NOT NULL DEFAULT 0;
