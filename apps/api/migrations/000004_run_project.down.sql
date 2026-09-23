DROP INDEX IF EXISTS workflow_runs_project_workflow_idx;
DROP INDEX IF EXISTS workflow_runs_project_created_idx;
ALTER TABLE workflow_runs DROP COLUMN project;
