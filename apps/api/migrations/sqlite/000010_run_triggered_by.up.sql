-- The SQLite half of the Postgres 000016 migration: who started a run.
ALTER TABLE workflow_runs ADD COLUMN triggered_by text NOT NULL DEFAULT '';
