-- A user who signs in through an external provider is matched on the
-- provider's stable id for the person — (auth_provider, auth_subject) — never
-- on a name or an email. link_state: see the Postgres migration 000030.
ALTER TABLE users ADD COLUMN auth_provider text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN auth_subject  text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN link_state    text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS users_auth_subject_key ON users (auth_provider, auth_subject) WHERE auth_subject <> '';
UPDATE users SET link_state = 'legacy' WHERE password_hash = '' AND instr(name, '@') > 0 AND auth_subject = '';
