-- The SQLite half of the Postgres 000019 migration: why a step was skipped.
ALTER TABLE step_runs ADD COLUMN skip_reason text NOT NULL DEFAULT '';
