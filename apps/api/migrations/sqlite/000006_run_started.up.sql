-- The SQLite half of the Postgres 000012 migration: when a run actually began.
ALTER TABLE workflow_runs ADD COLUMN started_at timestamp;

-- Backfill: for runs that already exist, the best available start is when their
-- FIRST STEP began — which is exactly the proxy the UI was computing client
-- side. Doing it once here means no consumer has to keep the proxy around.
UPDATE workflow_runs SET started_at = (
    SELECT min(started_at) FROM step_runs WHERE step_runs.run_id = workflow_runs.id
) WHERE started_at IS NULL;
