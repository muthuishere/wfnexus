-- WHEN a run actually began, as a fact on the run.
--
-- created_at is when the row was inserted — a queued run has one and has not
-- started. The UI was reading the FIRST STEP's started_at as a proxy, which is
-- a different thing (it is when the first step began, and it is absent for a
-- run that failed before any step ran). This is the run's own start: stamped
-- once, on the first transition out of `queued`.
ALTER TABLE workflow_runs ADD COLUMN started_at timestamptz;

-- Backfill: for runs that already exist, the best available start is when their
-- FIRST STEP began — which is exactly the proxy the UI was computing client
-- side. Doing it once here means no consumer has to keep the proxy around.
UPDATE workflow_runs SET started_at = (
    SELECT min(started_at) FROM step_runs WHERE step_runs.run_id = workflow_runs.id
) WHERE started_at IS NULL;
