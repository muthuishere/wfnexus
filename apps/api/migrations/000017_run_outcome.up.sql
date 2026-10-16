-- The run's headline verdict: one line, `ok|incident|waiting|failed: <text>`.
-- Written by a `run:` step through the file named by $WFX_OUTCOME, or derived
-- by the engine when the run ends (see engine/story.go). Empty for runs from
-- before this column.
ALTER TABLE workflow_runs ADD COLUMN outcome text NOT NULL DEFAULT '';
