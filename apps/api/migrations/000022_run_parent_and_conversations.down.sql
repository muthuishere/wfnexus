DROP TABLE IF EXISTS conversations;
ALTER TABLE workflow_memory DROP COLUMN conversation;
DROP INDEX IF EXISTS workflow_runs_parent_idx;
ALTER TABLE workflow_runs DROP COLUMN conversation;
ALTER TABLE workflow_runs DROP COLUMN parent_mode;
ALTER TABLE workflow_runs DROP COLUMN parent_step_id;
ALTER TABLE workflow_runs DROP COLUMN parent_run_id;
