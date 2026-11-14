-- See the Postgres migration 000031.
ALTER TABLE users ADD COLUMN auth_claim    text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN link_rejected text NOT NULL DEFAULT '';
