-- The SQLite half of the Postgres 000009 migration: who resolved a pause and
-- what they decided (ADR 0021). Same columns, this dialect's types.
ALTER TABLE step_runs ADD COLUMN resolved_by       text NOT NULL DEFAULT '';
ALTER TABLE step_runs ADD COLUMN resolved_at       timestamp;
ALTER TABLE step_runs ADD COLUMN resolution        text NOT NULL DEFAULT '';
ALTER TABLE step_runs ADD COLUMN resolution_reason text NOT NULL DEFAULT '';
