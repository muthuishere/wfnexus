DROP INDEX IF EXISTS users_auth_subject_key;
ALTER TABLE users DROP COLUMN link_state;
ALTER TABLE users DROP COLUMN auth_subject;
ALTER TABLE users DROP COLUMN auth_provider;
