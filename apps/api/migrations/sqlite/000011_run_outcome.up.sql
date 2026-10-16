-- The SQLite half of the Postgres 000017 migration: the run's headline verdict.
ALTER TABLE workflow_runs ADD COLUMN outcome text NOT NULL DEFAULT '';
